"""The runner's configuration, read from the environment the operator sets.

docs/transformations.md in this repository is the contract; every variable
here is listed there.
"""

from __future__ import annotations

import json
import os
from dataclasses import dataclass, field

MODES = ("run", "on-change", "maintain")
COMMANDS = ("run", "build", "test", "snapshot", "seed")
# dbt accepts --full-refresh only on these.
FULL_REFRESH_COMMANDS = ("run", "build", "seed")


class ConfigError(ValueError):
    pass


def _get(env: dict[str, str], name: str, default: str | None = None) -> str:
    value = env.get(name, "")
    if value == "":
        if default is None:
            raise ConfigError(f"{name} is not set")
        return default
    return value


def _list(value: str) -> list[str]:
    return [v.strip() for v in value.split(",") if v.strip()]


def _bool(value: str) -> bool:
    return value.strip().lower() in ("1", "true", "yes")


@dataclass(frozen=True)
class Trino:
    host: str
    port: int
    catalog: str
    ca_file: str
    token_file: str


@dataclass(frozen=True)
class Git:
    url: str
    ref: str
    path: str
    token: str = field(default="", repr=False)
    username: str = "x-access-token"


@dataclass(frozen=True)
class Dbt:
    command: str
    target: str
    schema: str
    threads: int
    select: tuple[str, ...] = ()
    exclude: tuple[str, ...] = ()
    full_refresh: bool = False
    vars: str = ""
    profiles_dir: str = ""


@dataclass(frozen=True)
class Config:
    name: str
    lane: str
    mode: str
    namespace: str
    state_configmap: str
    work_dir: str
    trino: Trino
    git: Git
    dbt: Dbt
    watch_sources: tuple[str, ...] = ()
    maintain_namespaces: tuple[str, ...] = ()
    file_size_threshold: str = "128MB"
    snapshot_retention: str = "7d"


def parse_vars(raw: str) -> str:
    """DBT_VARS is a JSON object; an empty one is the same as none."""
    if not raw.strip():
        return ""
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as e:
        raise ConfigError(f"DBT_VARS is not JSON: {e}") from e
    if not isinstance(value, dict):
        raise ConfigError("DBT_VARS must be a JSON object")
    return json.dumps(value, sort_keys=True) if value else ""


def load(mode: str, env: dict[str, str] | None = None) -> Config:
    env = dict(os.environ if env is None else env)
    if mode not in MODES:
        raise ConfigError(f"mode {mode!r} is not one of {', '.join(MODES)}")

    name = _get(env, "TRANSFORM_NAME")
    command = _get(env, "DBT_COMMAND", "run")
    if command not in COMMANDS:
        raise ConfigError(f"DBT_COMMAND {command!r} is not one of {', '.join(COMMANDS)}")
    try:
        port = int(_get(env, "TRINO_PORT", "8443"))
        threads = int(_get(env, "DBT_THREADS", "4"))
    except ValueError as e:
        raise ConfigError(f"TRINO_PORT and DBT_THREADS must be numbers: {e}") from e

    conf = Config(
        name=name,
        lane=_get(env, "TRANSFORM_LANE"),
        mode=mode,
        namespace=_get(env, "TRANSFORM_NAMESPACE"),
        state_configmap=_get(env, "TRANSFORM_STATE_CONFIGMAP", f"transform-{name}-state"),
        work_dir=_get(env, "HOME", "/work"),
        trino=Trino(
            host=_get(env, "TRINO_HOST"),
            port=port,
            catalog=_get(env, "TRINO_CATALOG", "lakekeeper"),
            ca_file=_get(env, "TRINO_CA_FILE", "/etc/trino-ca/ca.crt"),
            token_file=_get(env, "TRINO_TOKEN_FILE", "/var/run/secrets/opsarray/token"),
        ),
        git=Git(
            url=_get(env, "PROJECT_GIT_URL", ""),
            ref=_get(env, "PROJECT_GIT_REF", "main"),
            path=_get(env, "PROJECT_PATH", "").strip("/"),
            token=_get(env, "PROJECT_GIT_TOKEN", ""),
            username=_get(env, "PROJECT_GIT_USERNAME", "x-access-token"),
        ),
        dbt=Dbt(
            command=command,
            target=_get(env, "DBT_TARGET", "prod"),
            schema=_get(env, "DBT_SCHEMA", name.replace("-", "_")),
            threads=threads,
            select=tuple(_get(env, "DBT_SELECT", "").split()),
            exclude=tuple(_get(env, "DBT_EXCLUDE", "").split()),
            full_refresh=_bool(_get(env, "DBT_FULL_REFRESH", "false")),
            vars=parse_vars(_get(env, "DBT_VARS", "")),
            profiles_dir=_get(env, "DBT_PROFILES_DIR", ""),
        ),
        watch_sources=tuple(_list(_get(env, "WATCH_SOURCES", ""))),
        maintain_namespaces=tuple(_list(_get(env, "MAINTAIN_NAMESPACES", ""))),
        file_size_threshold=_get(env, "MAINTAIN_FILE_SIZE_THRESHOLD", "128MB"),
        snapshot_retention=_get(env, "MAINTAIN_SNAPSHOT_RETENTION", "7d"),
    )

    if mode in ("run", "on-change") and not conf.git.url:
        raise ConfigError("PROJECT_GIT_URL is not set")
    if mode == "on-change" and not conf.watch_sources:
        raise ConfigError("WATCH_SOURCES is not set")
    if mode == "maintain" and not conf.maintain_namespaces:
        raise ConfigError("MAINTAIN_NAMESPACES is not set")
    return conf
