"""Iceberg upkeep for the tables a Transformation writes.

Incremental merges leave many small files and a snapshot per run; on a lane
that runs every minute that is over a thousand snapshots a day. This
compacts the files and drops snapshots and unreferenced files older than the
retention.
"""

from __future__ import annotations

import logging

from .config import Config
from .trino_conn import base_tables, quote_ident, quote_literal

log = logging.getLogger(__name__)


def statements(catalog: str, ns: str, table: str, file_size_threshold: str, retention: str) -> list[str]:
    t = quote_ident(catalog, ns, table)
    return [
        f"ALTER TABLE {t} EXECUTE optimize(file_size_threshold => {quote_literal(file_size_threshold)})",
        f"ALTER TABLE {t} EXECUTE expire_snapshots(retention_threshold => {quote_literal(retention)})",
        f"ALTER TABLE {t} EXECUTE remove_orphan_files(retention_threshold => {quote_literal(retention)})",
    ]


def maintain(conn, conf: Config) -> int:
    """Returns the number of tables that failed; one failure does not stop
    the rest."""
    catalog = conf.trino.catalog
    tables = base_tables(conn, catalog, conf.maintain_namespaces)
    found = {ns for ns, _ in tables}
    for ns in conf.maintain_namespaces:
        if ns not in found:
            log.warning("namespace %s has no tables (or does not exist, or is not readable)", ns)

    failures = 0
    for ns, table in tables:
        try:
            for sql in statements(catalog, ns, table, conf.file_size_threshold, conf.snapshot_retention):
                cur = conn.cursor()
                try:
                    cur.execute(sql)
                    cur.fetchall()
                finally:
                    cur.close()
            log.info("maintained %s.%s", ns, table)
        except Exception:
            log.exception("maintaining %s.%s failed", ns, table)
            failures += 1
    return failures
