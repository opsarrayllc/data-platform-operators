import datetime as dt

import pyarrow as pa
import pytest
from google.analytics.data_v1beta.types import MetricType
from pyiceberg.catalog.sql import SqlCatalog

from ga4_loader import config as cfg
from ga4_loader.__main__ import chunks
from ga4_loader.ga4 import strip_query, to_arrow
from ga4_loader.lake import Lake

LOADED = dt.datetime(2026, 11, 10, 7, tzinfo=dt.timezone.utc)
PAGES = cfg.Report("pages", ("date", "pagePath"), ("screenPageViews", "userEngagementDuration"))
TYPES = [MetricType.TYPE_INTEGER, MetricType.TYPE_SECONDS]


def test_snake():
    assert cfg.snake("sessionDefaultChannelGroup") == "session_default_channel_group"
    assert cfg.snake("customEvent:plan_type") == "custom_event_plan_type"
    assert cfg.snake("date") == "date"


@pytest.mark.parametrize(
    "report, error",
    [
        ({"name": "x", "dimensions": ["country"], "metrics": ["sessions"]}, "date"),
        ({"name": "x", "dimensions": ["date", "pageLocation"], "metrics": ["sessions"]}, "personal"),
        ({"name": "x", "dimensions": ["date", "customUser:email"], "metrics": ["sessions"]}, "personal"),
        ({"name": "x", "dimensions": ["date"], "metrics": []}, "metrics"),
        ({"name": "x", "dimensions": ["date"] + [f"d{i}" for i in range(9)], "metrics": ["sessions"]}, "9"),
        ({"name": "Bad-Name", "dimensions": ["date"], "metrics": ["sessions"]}, "snake_case"),
    ],
)
def test_report_validation(report, error):
    with pytest.raises(cfg.ConfigError, match=error):
        cfg.parse_reports({"reports": [report]})


def test_shipped_reports_are_valid():
    # GA4_REPORTS_FILE=<infrastructure>/kubernetes/shared-services/charts/orchestration/files/ga4-reports.yaml
    import os
    import yaml

    path = os.environ.get("GA4_REPORTS_FILE")
    if not path:
        pytest.skip("set GA4_REPORTS_FILE to check a deployed report config")
    with open(path) as f:
        assert cfg.parse_reports(yaml.safe_load(f))


def test_properties():
    props = cfg.parse_properties([{"id": "properties/123", "name": "www"}, 456])
    assert [p.id for p in props] == ["123", "456"]
    with pytest.raises(cfg.ConfigError, match="numeric"):
        cfg.parse_properties(["G-ABC123"])
    with pytest.raises(cfg.ConfigError, match="twice"):
        cfg.parse_properties([1, "1"])


def test_chunks():
    assert chunks(dt.date(2024, 11, 15), dt.date(2025, 2, 3), 1) == [
        (dt.date(2024, 11, 15), dt.date(2024, 11, 30)),
        (dt.date(2024, 12, 1), dt.date(2024, 12, 31)),
        (dt.date(2025, 1, 1), dt.date(2025, 1, 31)),
        (dt.date(2025, 2, 1), dt.date(2025, 2, 3)),
    ]
    assert chunks(dt.date(2024, 3, 1), dt.date(2025, 6, 30), 12) == [
        (dt.date(2024, 3, 1), dt.date(2025, 2, 28)),
        (dt.date(2025, 3, 1), dt.date(2025, 6, 30)),
    ]


def test_strip_query():
    assert strip_query("/checkout?email=a@b.c") == "/checkout"
    assert strip_query("/a#frag") == "/a"
    assert strip_query("/plain") == "/plain"


def test_to_arrow_types_and_paths():
    t = to_arrow("123", PAGES, [["20261108", "/x?token=s", "5", "12.5"]], TYPES, True, False, LOADED)
    row = t.to_pylist()[0]
    assert row["date"] == dt.date(2026, 11, 8)
    assert row["page_path"] == "/x"
    assert row["screen_page_views"] == 5 and isinstance(row["screen_page_views"], int)
    assert row["user_engagement_duration"] == 12.5
    assert row["subject_to_thresholding"] is True and row["sampled"] is False
    assert t.schema.field("property_id").nullable is False


class LocalLake(Lake):
    def __init__(self, tmp_path):
        super().__init__("http://unused/catalog", "w", "google_analytics", "", "us-east-1")
        self._cat = SqlCatalog("t", uri=f"sqlite:///{tmp_path}/c.db", warehouse=f"file://{tmp_path}")

    def catalog(self):
        return self._cat


def rows(lake):
    tbl = lake.catalog().load_table(("google_analytics", "pages")).scan().to_arrow()
    return sorted((r["property_id"], str(r["date"]), r["screen_page_views"]) for r in tbl.to_pylist())


def test_replace_is_idempotent_per_property_and_range(tmp_path):
    lake = LocalLake(tmp_path)
    lake.ensure_namespace()
    lake.ensure_namespace()  # already exists: no error
    d1, d2, d3 = dt.date(2026, 11, 7), dt.date(2026, 11, 8), dt.date(2026, 11, 9)

    def data(pid, days, views):
        return to_arrow(pid, PAGES, [[d.strftime("%Y%m%d"), "/", str(views), "1"] for d in days], TYPES, False, False, LOADED)

    lake.replace("pages", "1", d1, d2, data("1", [d1, d2], 10))
    lake.replace("pages", "2", d1, d2, data("2", [d1, d2], 20))
    # Re-load property 1 for d2..d3: d1 stays, d2 is replaced, d3 is added,
    # property 2 is untouched.
    lake.replace("pages", "1", d2, d3, data("1", [d2, d3], 11))
    lake.replace("pages", "1", d2, d3, data("1", [d2, d3], 11))
    assert rows(lake) == [
        ("1", "2026-11-07", 10), ("1", "2026-11-08", 11), ("1", "2026-11-09", 11),
        ("2", "2026-11-07", 20), ("2", "2026-11-08", 20),
    ]
    # An empty result clears the range.
    lake.replace("pages", "2", d1, d1, data("2", [], 0))
    assert ("2", "2026-11-07", 20) not in rows(lake)

    spec = lake.catalog().load_table(("google_analytics", "pages")).spec()
    assert [f.name for f in spec.fields] == ["property_id", "date_year"]


def test_new_metric_adds_column(tmp_path):
    lake = LocalLake(tmp_path)
    lake.ensure_namespace()
    d = dt.date(2026, 11, 8)
    lake.replace("pages", "1", d, d, to_arrow("1", PAGES, [["20261108", "/", "1", "1"]], TYPES, False, False, LOADED))
    wider = cfg.Report("pages", PAGES.dimensions, PAGES.metrics + ("eventCount",))
    lake.replace("pages", "1", d, d, to_arrow("1", wider, [["20261108", "/", "1", "1", "7"]],
                                              TYPES + [MetricType.TYPE_INTEGER], False, False, LOADED))
    t = lake.catalog().load_table(("google_analytics", "pages"))
    assert "event_count" in t.schema().column_names
    assert t.scan().to_arrow().to_pylist()[0]["event_count"] == 7


def test_empty_result_before_table_exists(tmp_path):
    lake = LocalLake(tmp_path)
    lake.ensure_namespace()
    d = dt.date(2026, 11, 8)
    lake.replace("pages", "1", d, d, to_arrow("1", PAGES, [], [], False, False, LOADED))
    with pytest.raises(Exception):
        lake.catalog().load_table(("google_analytics", "pages"))


def test_empty_arrow_has_schema():
    t = to_arrow("1", PAGES, [], TYPES, False, False, LOADED)
    assert t.num_rows == 0 and "page_path" in t.schema.names
    assert t.schema.field("screen_page_views").type == pa.int64()
