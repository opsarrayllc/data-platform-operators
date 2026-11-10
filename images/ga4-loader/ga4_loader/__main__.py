"""GA4 Data API → Iceberg (LakeKeeper REST catalog).

    python -m ga4_loader daily                       # last LOOKBACK_DAYS days
    python -m ga4_loader backfill --from 2021-01-01  # through yesterday
    python -m ga4_loader backfill --from 2021-01-01 --to 2021-12-31 --report pages

Every write replaces one property's rows for the dates it covers, so any run
can be repeated. A backfill stops cleanly when a property's daily quota runs
low and logs the date to resume from.
"""

from __future__ import annotations

import argparse
import datetime as dt
import logging
import os
import sys

from . import config as cfg
from .ga4 import GA4, QuotaExhausted
from .lake import Lake

log = logging.getLogger("ga4_loader")


def env(name: str, default: str | None = None) -> str:
    value = os.environ.get(name, default)
    if value is None:
        sys.exit(f"{name} is not set")
    return value


def parse_day(value: str) -> dt.date:
    return dt.date.fromisoformat(value)


def chunks(start: dt.date, end: dt.date, months: int) -> list[tuple[dt.date, dt.date]]:
    """Split [start, end] into ranges of whole calendar months."""
    out = []
    cur = start
    while cur <= end:
        y, m = divmod(cur.month - 1 + months, 12)
        nxt = dt.date(cur.year + y, m + 1, 1)
        out.append((cur, min(end, nxt - dt.timedelta(days=1))))
        cur = nxt
    return out


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="ga4_loader", description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("mode", choices=["daily", "backfill"])
    p.add_argument("--from", dest="start", type=parse_day, help="first day (backfill)")
    p.add_argument("--to", dest="end", type=parse_day, help="last day (default: yesterday)")
    p.add_argument("--report", action="append", help="only these reports (repeatable)")
    p.add_argument("--property", action="append", help="only these property ids (repeatable)")
    p.add_argument("--chunk-months", type=int, default=12,
                   help="backfill months per request and per write (default 12)")
    args = p.parse_args(argv)

    logging.basicConfig(level=os.environ.get("LOG_LEVEL", "INFO"),
                        format="%(asctime)s %(levelname)s %(name)s: %(message)s")

    conf = cfg.load(env("GA4_REPORTS_FILE", "/etc/ga4/reports.yaml"),
                    env("GA4_PROPERTIES_FILE", "/etc/ga4/properties.yaml"))
    reports = [r for r in conf.reports if not args.report or r.name in args.report]
    props = [x for x in conf.properties if not args.property or x.id in args.property]
    if args.report and len(reports) != len(set(args.report)):
        sys.exit(f"unknown report in {args.report}; configured: {[r.name for r in conf.reports]}")
    if not props:
        sys.exit("no matching properties")

    # Day boundaries are the property's own time zone; running after 06:00
    # UTC means "yesterday" is over in every US zone.
    yesterday = dt.datetime.now(dt.timezone.utc).date() - dt.timedelta(days=1)
    end = args.end or yesterday
    if args.mode == "daily":
        lookback = int(env("LOOKBACK_DAYS", "3"))
        ranges = [(end - dt.timedelta(days=lookback - 1), end)]
    else:
        if not args.start:
            sys.exit("backfill needs --from (the property's creation date is the earliest useful day)")
        ranges = chunks(args.start, end, args.chunk_months)

    lake = Lake(
        uri=env("LAKEKEEPER_CATALOG_URL"),
        warehouse=env("LAKEKEEPER_WAREHOUSE"),
        namespace=env("ICEBERG_NAMESPACE", "google_analytics"),
        token_file=env("LAKEKEEPER_TOKEN_FILE", "/var/run/secrets/lakekeeper/token"),
        region=env("AWS_REGION", "us-east-1"),
    )
    lake.register()
    lake.ensure_namespace()
    ga = GA4()

    loaded_at = dt.datetime.now(dt.timezone.utc)
    failures = 0
    for start, stop in ranges:
        for prop in props:
            for report in reports:
                label = f"{report.name} property={prop.id} {start}..{stop}"
                try:
                    result = ga.run(prop.id, report, start, stop, loaded_at)
                except QuotaExhausted as e:
                    log.error("%s; resume with --from %s", e, start)
                    return 2
                except Exception:
                    # One bad report (an incompatible dimension, a property
                    # without ecommerce) must not stop the rest.
                    log.exception("%s failed", label)
                    failures += 1
                    continue
                lake.replace(report.name, prop.id, start, stop, result.table)
                flags = " thresholded" * result.subject_to_thresholding + " sampled" * result.sampled
                log.info("%s: %d rows%s", label, result.rows, flags)
        log.info("completed through %s", stop)

    if failures:
        log.error("%d report loads failed", failures)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
