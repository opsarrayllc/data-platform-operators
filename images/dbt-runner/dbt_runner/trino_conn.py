"""Trino connections signed in with the pod's projected ServiceAccount token.

The kubelet rotates the token file well before the token expires, and a dbt
run or a long optimize can outlast one token. So the token is read from the
file on every HTTP request instead of once per connection, both for the
runner's own queries and for dbt's (see patch_dbt_trino).
"""

from __future__ import annotations

from typing import Any, Iterable

import requests
import trino
import trino.auth
import trino.dbapi

from .config import Trino


class _FileBearer(requests.auth.AuthBase):
    def __init__(self, path: str):
        self.path = path

    def __call__(self, r: requests.PreparedRequest) -> requests.PreparedRequest:
        with open(self.path) as f:
            r.headers["Authorization"] = "Bearer " + f.read().strip()
        return r


class FileJWTAuthentication(trino.auth.Authentication):
    def __init__(self, path: str):
        self.path = path

    def set_http_session(self, http_session: requests.Session) -> requests.Session:
        http_session.auth = _FileBearer(self.path)
        return http_session

    def get_exceptions(self) -> tuple[Any, ...]:
        return ()

    def __eq__(self, other: object) -> bool:
        return isinstance(other, FileJWTAuthentication) and other.path == self.path


def patch_dbt_trino(token_file: str) -> None:
    """Make dbt-trino's jwt method read the token from token_file per request.

    dbt-trino builds trino.auth.JWTAuthentication(jwt_token) from the profile,
    looked up on the module at connect time, so replacing that name is enough.
    The profile's jwt_token value is then ignored.
    """

    def factory(_token: str) -> FileJWTAuthentication:
        return FileJWTAuthentication(token_file)

    trino.auth.JWTAuthentication = factory  # type: ignore[assignment,misc]


def connect(t: Trino) -> trino.dbapi.Connection:
    # No user: Trino takes the principal from the token. Sending a user that
    # differs from it would be an impersonation request.
    return trino.dbapi.connect(
        host=t.host,
        port=t.port,
        http_scheme="https",
        auth=FileJWTAuthentication(t.token_file),
        verify=t.ca_file,
        catalog=t.catalog,
        source="opsarray-dbt-runner",
    )


def quote_ident(*parts: str) -> str:
    return ".".join('"' + p.replace('"', '""') + '"' for p in parts)


def quote_literal(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def query(conn: trino.dbapi.Connection, sql: str) -> list[list[Any]]:
    cur = conn.cursor()
    try:
        cur.execute(sql)
        return cur.fetchall()
    finally:
        cur.close()


def base_tables(conn: trino.dbapi.Connection, catalog: str, namespaces: Iterable[str]) -> list[tuple[str, str]]:
    """Every Iceberg table (not view) in the namespaces, sorted."""
    namespaces = sorted(set(namespaces))
    if not namespaces:
        return []
    sql = (
        f"SELECT table_schema, table_name FROM {quote_ident(catalog, 'information_schema', 'tables')} "
        f"WHERE table_type = 'BASE TABLE' AND table_schema IN ({', '.join(quote_literal(n) for n in namespaces)}) "
        "ORDER BY 1, 2"
    )
    return [(r[0], r[1]) for r in query(conn, sql)]
