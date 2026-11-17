"""dbt on Trino for data-orchestration-operator Transformations.

    python -m dbt_runner run         # clone the project, run the lane's dbt command
    python -m dbt_runner on-change   # the same, only when a watched table has new data
    python -m dbt_runner maintain    # optimize and expire snapshots in MAINTAIN_NAMESPACES

Configuration comes from the environment the operator sets; see
docs/transformations.md.
"""

from __future__ import annotations

import argparse
import logging
import os
import sys

from . import config as cfg
from . import state, watch
from .maintain import maintain
from .project import run_dbt
from .trino_conn import connect

log = logging.getLogger("dbt_runner")


def on_change(conf: cfg.Config) -> int:
    store = state.KubeConfigMap(conf.namespace, conf.state_configmap)
    key = state.lane_key(conf.lane)

    conn = connect(conf.trino)
    tables = watch.resolve(conn, conf.trino.catalog, conf.watch_sources)
    if not tables:
        log.warning("no watched tables found; nothing to do")
        return 0
    # Read before dbt runs: a commit that lands during the run is newer than
    # what gets recorded, so the next poll picks it up.
    current = watch.current_snapshots(conn, conf.trino.catalog, tables)
    conn.close()

    previous = state.decode(store.get(key))
    changed = watch.changed(previous, current)
    if not changed:
        log.info("no new snapshots in %d watched tables", len(tables))
        return 0
    shown = ", ".join(changed[:10]) + (f" and {len(changed) - 10} more" if len(changed) > 10 else "")
    log.info("new snapshots in %s", shown)

    if not run_dbt(conf):
        # State is left alone, so the next tick retries.
        return 1
    store.set(key, state.encode({**previous, **current}))
    return 0


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="dbt_runner", description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("mode", choices=cfg.MODES)
    args = p.parse_args(argv)

    logging.basicConfig(level=os.environ.get("LOG_LEVEL", "INFO"),
                        format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    # Logs that OAuth2 tokens cannot be cached; irrelevant with JWT.
    logging.getLogger("trino.auth").setLevel(logging.WARNING)
    try:
        conf = cfg.load(args.mode)
    except cfg.ConfigError as e:
        log.error("%s", e)
        return 2
    log.info("transformation %s, lane %s, mode %s", conf.name, conf.lane, conf.mode)

    if conf.mode == "run":
        return 0 if run_dbt(conf) else 1
    if conf.mode == "on-change":
        return on_change(conf)

    conn = connect(conf.trino)
    try:
        failures = maintain(conn, conf)
    finally:
        conn.close()
    if failures:
        log.error("%d tables failed", failures)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
