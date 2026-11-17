"""Fetching the dbt project and running dbt in this process.

dbt runs in-process (dbtRunner) rather than as a subprocess so the Trino
token patch in trino_conn applies to its connections.
"""

from __future__ import annotations

import base64
import logging
import os
import subprocess

import yaml

from .config import FULL_REFRESH_COMMANDS, Config, Dbt, Git, Trino
from .trino_conn import patch_dbt_trino

log = logging.getLogger(__name__)


def git_env(g: Git) -> dict[str, str]:
    """Environment for git: the token goes in as an HTTP header through
    GIT_CONFIG_*, so it is neither in the URL nor on a command line."""
    env = {**os.environ, "GIT_TERMINAL_PROMPT": "0"}
    if g.token:
        basic = base64.b64encode(f"{g.username}:{g.token}".encode()).decode()
        env.update(
            GIT_CONFIG_COUNT="1",
            GIT_CONFIG_KEY_0="http.extraHeader",
            GIT_CONFIG_VALUE_0=f"Authorization: Basic {basic}",
        )
    return env


def clone(g: Git, dest: str) -> str:
    """Shallow-fetch one ref (branch, tag or commit) into dest; returns the
    directory holding dbt_project.yml."""
    env = git_env(g)

    def git(*args: str) -> str:
        return subprocess.run(
            ["git", "-C", dest, *args], env=env, check=True, capture_output=True, text=True
        ).stdout.strip()

    os.makedirs(dest, exist_ok=True)
    try:
        git("init", "-q")
        git("remote", "add", "origin", g.url)
        git("fetch", "-q", "--depth", "1", "origin", g.ref)
        git("checkout", "-q", "FETCH_HEAD")
        sha = git("rev-parse", "HEAD")
    except subprocess.CalledProcessError as e:
        raise SystemExit(f"fetching {g.url} at {g.ref} failed: {e.stderr.strip()}") from e
    log.info("project %s at %s (%s)", g.url, g.ref, sha[:12])

    project_dir = os.path.join(dest, g.path) if g.path else dest
    if not os.path.isfile(os.path.join(project_dir, "dbt_project.yml")):
        raise SystemExit(f"no dbt_project.yml in {g.path or 'the repository root'}")
    return project_dir


def profile_name(project_dir: str) -> str:
    with open(os.path.join(project_dir, "dbt_project.yml")) as f:
        name = (yaml.safe_load(f) or {}).get("profile")
    if not name:
        raise SystemExit("dbt_project.yml has no profile")
    return name


def profiles(profile: str, d: Dbt, t: Trino) -> dict:
    """profiles.yml for the project. jwt_token is a placeholder: the patched
    authentication reads the token file instead."""
    return {
        profile: {
            "target": d.target,
            "outputs": {
                d.target: {
                    "type": "trino",
                    "method": "jwt",
                    "jwt_token": "projected-serviceaccount-token",
                    "host": t.host,
                    "port": t.port,
                    "database": t.catalog,
                    "schema": d.schema,
                    "threads": d.threads,
                    "cert": t.ca_file,
                }
            },
        }
    }


def write_profiles(project_dir: str, conf: Config) -> str:
    """Write profiles.yml unless DBT_PROFILES_DIR names the project's own."""
    if conf.dbt.profiles_dir:
        return conf.dbt.profiles_dir
    path = os.path.join(conf.work_dir, "profiles")
    os.makedirs(path, exist_ok=True)
    with open(os.path.join(path, "profiles.yml"), "w") as f:
        yaml.safe_dump(profiles(profile_name(project_dir), conf.dbt, conf.trino), f, sort_keys=False)
    return path


def dbt_args(d: Dbt, project_dir: str, profiles_dir: str) -> list[str]:
    args = [d.command, "--project-dir", project_dir, "--profiles-dir", profiles_dir, "--target", d.target]
    if d.select:
        args += ["--select", *d.select]
    if d.exclude:
        args += ["--exclude", *d.exclude]
    if d.vars:
        args += ["--vars", d.vars]
    if d.full_refresh:
        if d.command in FULL_REFRESH_COMMANDS:
            args.append("--full-refresh")
        else:
            log.warning("ignoring DBT_FULL_REFRESH: dbt %s has no --full-refresh", d.command)
    return args


def invoke(args: list[str]) -> bool:
    from dbt.cli.main import dbtRunner  # slow import; only when dbt runs

    log.info("dbt %s", " ".join(args))
    res = dbtRunner().invoke(args)
    if res.exception is not None:
        log.error("dbt %s failed: %s", args[0], res.exception)
    return bool(res.success)


def run_dbt(conf: Config) -> bool:
    """Clone, install packages and run the lane's dbt command."""
    project_dir = clone(conf.git, os.path.join(conf.work_dir, "project"))
    profiles_dir = write_profiles(project_dir, conf)
    patch_dbt_trino(conf.trino.token_file)

    if any(os.path.isfile(os.path.join(project_dir, f)) for f in ("packages.yml", "dependencies.yml")):
        if not invoke(["deps", "--project-dir", project_dir, "--profiles-dir", profiles_dir]):
            return False
    return invoke(dbt_args(conf.dbt, project_dir, profiles_dir))
