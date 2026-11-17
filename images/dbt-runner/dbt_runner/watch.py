"""on-change gating: has any watched table committed new data?

A watched source is a namespace (every table in it) or "namespace.table".
For each table the newest snapshot that is not a "replace" counts; replace
snapshots come from compaction (optimize), which rewrites files without
changing the data, so maintenance does not wake every lane.
"""

from __future__ import annotations

import logging
from typing import Iterable

from .trino_conn import base_tables, query, quote_ident, quote_literal

log = logging.getLogger(__name__)

# Tables per query. Each is a small metadata scan; one UNION ALL query keeps
# a poll of a large namespace to a single round trip per batch.
BATCH = 50


def split_sources(sources: Iterable[str]) -> tuple[list[str], list[tuple[str, str]]]:
    namespaces, tables = [], []
    for s in sources:
        ns, sep, table = s.partition(".")
        if sep:
            tables.append((ns, table))
        else:
            namespaces.append(ns)
    return namespaces, tables


def resolve(conn, catalog: str, sources: Iterable[str]) -> list[tuple[str, str]]:
    """Expand the watched sources to existing tables. Missing ones are logged."""
    namespaces, tables = split_sources(sources)
    known = base_tables(conn, catalog, set(namespaces) | {ns for ns, _ in tables})
    found = {ns for ns, _ in known}
    for ns in namespaces:
        if ns not in found:
            log.warning("watched namespace %s has no tables (or does not exist, or is not readable)", ns)
    known_set = set(known)
    for t in tables:
        if t not in known_set:
            log.warning("watched table %s.%s does not exist or is not readable", *t)
    wanted = set(namespaces)
    return sorted({t for t in known if t[0] in wanted} | (set(tables) & known_set))


def snapshots_sql(catalog: str, tables: list[tuple[str, str]]) -> str:
    parts = [
        f"SELECT {quote_literal(f'{ns}.{t}')}, CAST(max_by(snapshot_id, committed_at) AS varchar) "
        f"FROM {quote_ident(catalog, ns, t + '$snapshots')} WHERE operation <> 'replace'"
        for ns, t in tables
    ]
    return "\nUNION ALL\n".join(parts)


def current_snapshots(conn, catalog: str, tables: list[tuple[str, str]]) -> dict[str, str]:
    """Table -> id of its newest data snapshot. Empty tables are left out."""
    out: dict[str, str] = {}
    for i in range(0, len(tables), BATCH):
        for name, snapshot in query(conn, snapshots_sql(catalog, tables[i : i + BATCH])):
            if snapshot is not None:
                out[name] = snapshot
    return out


def changed(previous: dict[str, str], current: dict[str, str]) -> list[str]:
    """Tables whose newest snapshot differs from the one last processed."""
    return sorted(t for t, s in current.items() if previous.get(t) != s)
