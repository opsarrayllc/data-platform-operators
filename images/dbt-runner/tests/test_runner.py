import base64
import datetime as dt
import json
import os
import subprocess

import pytest
import requests
import yaml

from dbt_runner import __main__ as runner
from dbt_runner import config as cfg
from dbt_runner import maintain, project, state, trino_conn, watch

BASE_ENV = {
    "TRANSFORM_NAME": "keyme",
    "TRANSFORM_LANE": "live",
    "TRANSFORM_NAMESPACE": "argo",
    "TRANSFORM_STATE_CONFIGMAP": "transform-keyme-state",
    "TRINO_HOST": "trino.trino.svc",
    "TRINO_PORT": "8443",
    "TRINO_CATALOG": "lakekeeper",
    "PROJECT_GIT_URL": "https://example.com/dbt.git",
    "DBT_COMMAND": "build",
    "DBT_SELECT": "tag:live  tag:core",
    "DBT_VARS": '{"b": 1, "a": "x"}',
    "WATCH_SOURCES": "atlantis,lks.lks_public_users",
    "HOME": "/work",
}


def conf(mode="on-change", **env):
    return cfg.load(mode, {**BASE_ENV, **env})


# --- config ---------------------------------------------------------------


def test_load():
    c = conf()
    assert c.dbt.select == ("tag:live", "tag:core")
    assert c.dbt.vars == '{"a": "x", "b": 1}'
    assert c.dbt.schema == "keyme"
    assert c.dbt.target == "prod"
    assert c.watch_sources == ("atlantis", "lks.lks_public_users")
    assert c.trino.token_file == "/var/run/secrets/opsarray/token"
    assert "s3cr3t" not in repr(cfg.load("run", {**BASE_ENV, "PROJECT_GIT_TOKEN": "s3cr3t"}))


@pytest.mark.parametrize(
    "mode, env, error",
    [
        ("bogus", {}, "mode"),
        ("run", {"DBT_COMMAND": "compile"}, "DBT_COMMAND"),
        ("run", {"PROJECT_GIT_URL": ""}, "PROJECT_GIT_URL"),
        ("on-change", {"WATCH_SOURCES": ""}, "WATCH_SOURCES"),
        ("maintain", {}, "MAINTAIN_NAMESPACES"),
        ("run", {"DBT_VARS": "[1]"}, "object"),
        ("run", {"DBT_VARS": "{nope"}, "JSON"),
        ("run", {"TRINO_HOST": ""}, "TRINO_HOST"),
    ],
)
def test_load_errors(mode, env, error):
    with pytest.raises(cfg.ConfigError, match=error):
        cfg.load(mode, {**BASE_ENV, **env})


def test_empty_vars():
    assert conf(DBT_VARS="{}").dbt.vars == ""


# --- dbt invocation -------------------------------------------------------


def test_dbt_args():
    c = conf(DBT_FULL_REFRESH="true", DBT_EXCLUDE="tag:slow")
    assert project.dbt_args(c.dbt, "/work/project", "/work/profiles") == [
        "build", "--project-dir", "/work/project", "--profiles-dir", "/work/profiles", "--target", "prod",
        "--select", "tag:live", "tag:core", "--exclude", "tag:slow", "--vars", '{"a": "x", "b": 1}',
        "--full-refresh",
    ]
    # test has no --full-refresh; the flag is dropped rather than failing dbt.
    t = conf(DBT_COMMAND="test", DBT_FULL_REFRESH="true", DBT_SELECT="", DBT_VARS="")
    assert project.dbt_args(t.dbt, "p", "q") == ["test", "--project-dir", "p", "--profiles-dir", "q", "--target", "prod"]


def test_profiles():
    c = conf(DBT_SCHEMA="analytics")
    out = project.profiles("keyme", c.dbt, c.trino)["keyme"]
    assert out["target"] == "prod"
    assert out["outputs"]["prod"] == {
        "type": "trino",
        "method": "jwt",
        "jwt_token": "projected-serviceaccount-token",
        "host": "trino.trino.svc",
        "port": 8443,
        "database": "lakekeeper",
        "schema": "analytics",
        "threads": 4,
        "cert": "/etc/trino-ca/ca.crt",
    }


def test_git_env_keeps_token_out_of_url():
    c = cfg.load("run", {**BASE_ENV, "PROJECT_GIT_TOKEN": "s3cr3t"})
    env = project.git_env(c.git)
    assert env["GIT_CONFIG_KEY_0"] == "http.extraHeader"
    basic = env["GIT_CONFIG_VALUE_0"].removeprefix("Authorization: Basic ")
    assert base64.b64decode(basic).decode() == "x-access-token:s3cr3t"
    assert "GIT_CONFIG_COUNT" not in project.git_env(conf().git)


DBT_PROJECT = """
name: sample
profile: sample_profile
version: "1.0"
"""


@pytest.fixture
def repo(tmp_path):
    """A local git repository holding a tiny dbt project under dbt/."""
    src = tmp_path / "src"
    (src / "dbt" / "models").mkdir(parents=True)
    (src / "dbt" / "dbt_project.yml").write_text(DBT_PROJECT)
    (src / "dbt" / "models" / "orders.sql").write_text("{{ config(tags=['live']) }} select 1 as id")

    def git(*args):
        subprocess.run(["git", "-C", str(src), *args], check=True, capture_output=True,
                       env={**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
                            "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"})

    git("init", "-q", "-b", "main")
    git("add", ".")
    git("commit", "-q", "-m", "init")
    git("tag", "v1")
    return src


@pytest.mark.parametrize("ref", ["main", "v1", "HEAD-SHA"])
def test_clone_and_parse(repo, tmp_path, ref):
    if ref == "HEAD-SHA":
        ref = subprocess.run(["git", "-C", str(repo), "rev-parse", "HEAD"], check=True,
                             capture_output=True, text=True).stdout.strip()
    work = tmp_path / "work"
    c = cfg.load("run", {**BASE_ENV, "HOME": str(work), "PROJECT_GIT_URL": f"file://{repo}",
                         "PROJECT_GIT_REF": ref, "PROJECT_PATH": "/dbt/"})
    project_dir = project.clone(c.git, str(work / "project"))
    assert project_dir == str(work / "project" / "dbt")

    profiles_dir = project.write_profiles(project_dir, c)
    written = yaml.safe_load((work / "profiles" / "profiles.yml").read_text())
    assert list(written) == ["sample_profile"]

    # parse loads the profile through dbt-trino without connecting.
    trino_conn.patch_dbt_trino("/nonexistent")
    assert project.invoke(["parse", "--project-dir", project_dir, "--profiles-dir", profiles_dir])


def test_clone_missing_project(repo, tmp_path):
    c = cfg.load("run", {**BASE_ENV, "PROJECT_GIT_URL": f"file://{repo}", "PROJECT_PATH": "nope"})
    with pytest.raises(SystemExit, match="no dbt_project.yml"):
        project.clone(c.git, str(tmp_path / "p"))


def test_clone_bad_ref(repo, tmp_path):
    c = cfg.load("run", {**BASE_ENV, "PROJECT_GIT_URL": f"file://{repo}", "PROJECT_GIT_REF": "nope"})
    with pytest.raises(SystemExit, match="failed"):
        project.clone(c.git, str(tmp_path / "p"))


# --- Trino auth -----------------------------------------------------------


def test_file_bearer_reads_token_per_request(tmp_path):
    token = tmp_path / "token"
    token.write_text("one\n")
    session = trino_conn.FileJWTAuthentication(str(token)).set_http_session(requests.Session())
    req = requests.Request("GET", "https://trino/").prepare()
    assert session.auth(req).headers["Authorization"] == "Bearer one"
    token.write_text("two")
    assert session.auth(req).headers["Authorization"] == "Bearer two"


def test_patch_dbt_trino(tmp_path):
    from dbt.adapters.trino.connections import TrinoJwtCredentials

    trino_conn.patch_dbt_trino(str(tmp_path / "token"))
    creds = TrinoJwtCredentials(database="lakekeeper", schema="s", host="h", port=8443, jwt_token="ignored")
    auth = creds.trino_auth()
    assert isinstance(auth, trino_conn.FileJWTAuthentication)
    assert auth.path == str(tmp_path / "token")


def test_quoting():
    assert trino_conn.quote_ident("cat", 'we"ird', "t$snapshots") == '"cat"."we""ird"."t$snapshots"'
    assert trino_conn.quote_literal("o'brien") == "'o''brien'"


# --- on-change ------------------------------------------------------------


class FakeConn:
    """Answers information_schema and $snapshots queries from a dict."""

    def __init__(self, tables, snapshots=None, fail_on=None):
        self.tables = tables  # [(ns, table)]
        self.snapshots = snapshots or {}  # "ns.t" -> id
        self.fail_on = fail_on
        self.executed = []
        self.closed = False

    def cursor(self):
        return FakeCursor(self)

    def close(self):
        self.closed = True


class FakeCursor:
    def __init__(self, conn):
        self.conn = conn
        self.rows = []

    def execute(self, sql):
        self.conn.executed.append(sql)
        if self.conn.fail_on and self.conn.fail_on in sql:
            raise RuntimeError("boom")
        if "information_schema" in sql:
            self.rows = [[ns, t] for ns, t in sorted(self.conn.tables) if f"'{ns}'" in sql]
        elif "$snapshots" in sql:
            self.rows = []
            for part in sql.split("UNION ALL"):
                name = part.split("'")[1]
                self.rows.append([name, self.conn.snapshots.get(name)])
        else:
            self.rows = [[True]]

    def fetchall(self):
        return self.rows

    def close(self):
        pass


TABLES = [("atlantis", "a"), ("atlantis", "b"), ("lks", "lks_public_users"), ("lks", "other")]


def test_split_sources():
    assert watch.split_sources(["atlantis", "lks.users"]) == (["atlantis"], [("lks", "users")])


def test_resolve(caplog):
    conn = FakeConn(TABLES)
    got = watch.resolve(conn, "lakekeeper", ["atlantis", "lks.lks_public_users", "lks.missing", "empty"])
    assert got == [("atlantis", "a"), ("atlantis", "b"), ("lks", "lks_public_users")]
    assert "lks.missing" in caplog.text and "empty" in caplog.text


def test_snapshots_sql():
    sql = watch.snapshots_sql("lakekeeper", [("atlantis", "a"), ("lks", "u")])
    assert sql.count("UNION ALL") == 1
    assert '"lakekeeper"."atlantis"."a$snapshots"' in sql
    assert "operation <> 'replace'" in sql
    assert "max_by(snapshot_id, committed_at)" in sql


def test_current_snapshots_batches(monkeypatch):
    monkeypatch.setattr(watch, "BATCH", 2)
    tables = [("ns", f"t{i}") for i in range(5)]
    conn = FakeConn(tables, {f"ns.t{i}": str(i) for i in range(4)})  # t4 is empty
    assert watch.current_snapshots(conn, "c", tables) == {f"ns.t{i}": str(i) for i in range(4)}
    assert len(conn.executed) == 3


def test_changed():
    assert watch.changed({}, {"a": "1"}) == ["a"]
    assert watch.changed({"a": "1", "gone": "9"}, {"a": "1"}) == []
    assert watch.changed({"a": "1", "b": "2"}, {"a": "1", "b": "3", "c": "4"}) == ["b", "c"]


def test_state_codec():
    now = dt.datetime(2026, 11, 17, 12, tzinfo=dt.timezone.utc)
    raw = state.encode({"b": "2", "a": "1"}, now)
    assert json.loads(raw) == {"snapshots": {"a": "1", "b": "2"}, "processedAt": "2026-11-17T12:00:00Z"}
    assert state.decode(raw) == {"a": "1", "b": "2"}
    for bad in (None, "", "nope", "[]", '{"snapshots": 1}'):
        assert state.decode(bad) == {}


class FakeResponse:
    def __init__(self, body=None, status=200):
        self.body = body or {}
        self.status = status

    def json(self):
        return self.body

    def raise_for_status(self):
        if self.status >= 400:
            raise requests.HTTPError(str(self.status))


class FakeSession:
    def __init__(self, data=None):
        self.data = dict(data or {})
        self.calls = []

    def get(self, url, headers, verify, timeout):
        self.calls.append(("GET", url, headers))
        return FakeResponse({"data": self.data})

    def patch(self, url, data, headers, verify, timeout):
        self.calls.append(("PATCH", url, headers))
        self.data.update(json.loads(data)["data"])
        return FakeResponse()


def test_kube_configmap():
    session = FakeSession({"live.json": "x"})
    cm = state.KubeConfigMap("argo", "transform-keyme-state", session=session, base_url="https://k8s",
                             token=lambda: "tok", verify=False)
    assert cm.get("live.json") == "x"
    assert cm.get("other.json") is None
    cm.set("live.json", "y")
    assert session.data["live.json"] == "y"
    method, url, headers = session.calls[-1]
    assert url == "https://k8s/api/v1/namespaces/argo/configmaps/transform-keyme-state"
    assert headers == {"Authorization": "Bearer tok", "Content-Type": "application/merge-patch+json"}


@pytest.fixture
def on_change_env(monkeypatch):
    """on_change with a fake Trino, a fake ConfigMap and dbt stubbed out."""
    session = FakeSession()
    conn = FakeConn(TABLES, {"atlantis.a": "1", "atlantis.b": "2", "lks.lks_public_users": "3"})
    dbt_calls = []
    result = {"ok": True}

    monkeypatch.setattr(runner, "connect", lambda t: conn)
    real = state.KubeConfigMap
    monkeypatch.setattr(
        runner.state, "KubeConfigMap",
        lambda ns, name: real(ns, name, session=session, base_url="https://k8s", token=lambda: "t", verify=False),
    )

    def fake_run(c):
        dbt_calls.append(c.lane)
        return result["ok"]

    monkeypatch.setattr(runner, "run_dbt", fake_run)
    return session, conn, dbt_calls, result


def test_on_change(on_change_env):
    session, conn, dbt_calls, result = on_change_env
    c = conf()

    # First run: no state, so dbt runs and the snapshots are recorded.
    assert runner.on_change(c) == 0
    assert dbt_calls == ["live"]
    assert state.decode(session.data["live.json"]) == {
        "atlantis.a": "1", "atlantis.b": "2", "lks.lks_public_users": "3"}

    # Nothing new: dbt does not run.
    assert runner.on_change(c) == 0
    assert dbt_calls == ["live"]

    # A new commit, but dbt fails: state is unchanged so the next tick retries.
    conn.snapshots["atlantis.b"] = "5"
    result["ok"] = False
    assert runner.on_change(c) == 1
    assert state.decode(session.data["live.json"])["atlantis.b"] == "2"

    result["ok"] = True
    assert runner.on_change(c) == 0
    assert dbt_calls == ["live", "live", "live"]
    assert state.decode(session.data["live.json"])["atlantis.b"] == "5"


def test_on_change_nothing_to_watch(on_change_env):
    session, conn, dbt_calls, _ = on_change_env
    assert runner.on_change(conf(WATCH_SOURCES="missing")) == 0
    assert dbt_calls == []
    assert "live.json" not in session.data


# --- maintain -------------------------------------------------------------


def test_statements():
    assert maintain.statements("lakekeeper", "ns", "t", "128MB", "7d") == [
        """ALTER TABLE "lakekeeper"."ns"."t" EXECUTE optimize(file_size_threshold => '128MB')""",
        """ALTER TABLE "lakekeeper"."ns"."t" EXECUTE expire_snapshots(retention_threshold => '7d')""",
        """ALTER TABLE "lakekeeper"."ns"."t" EXECUTE remove_orphan_files(retention_threshold => '7d')""",
    ]


def test_maintain_continues_after_failure():
    conn = FakeConn(TABLES, fail_on='"atlantis"."a" EXECUTE optimize')
    c = cfg.load("maintain", {**BASE_ENV, "MAINTAIN_NAMESPACES": "atlantis,lks"})
    assert maintain.maintain(conn, c) == 1
    altered = [s for s in conn.executed if s.startswith("ALTER")]
    # a failed on its first statement; b and both lks tables got all three.
    assert len(altered) == 1 + 3 * 3


def test_main_config_error(monkeypatch):
    for k in BASE_ENV:
        monkeypatch.delenv(k, raising=False)
    assert runner.main(["run"]) == 2
