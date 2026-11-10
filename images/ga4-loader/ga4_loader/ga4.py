"""GA4 Data API reads, turned into Arrow tables."""

from __future__ import annotations

import datetime as dt
import logging
import time
from dataclasses import dataclass

import pyarrow as pa
from google.analytics.data_v1beta import BetaAnalyticsDataClient
from google.analytics.data_v1beta.types import (
    DateRange,
    Dimension,
    Metric,
    MetricType,
    RunReportRequest,
)
from google.api_core import exceptions as gexc

from .config import PATH_DIMENSIONS, Report, snake

log = logging.getLogger(__name__)

# Rows per request. The API allows 250,000; smaller pages keep each response
# well inside the default gRPC message size.
PAGE_SIZE = 100_000

# Stop a run before the property's daily allowance is gone, so analysts'
# own GA4 usage on the property keeps working. Standard properties get
# 200,000 core tokens a day and 40,000 an hour.
MIN_DAILY_TOKENS = 5_000
MIN_HOURLY_TOKENS = 2_000
HOURLY_WAIT_SECONDS = 15 * 60

INTEGER_TYPES = {MetricType.TYPE_INTEGER}


class QuotaExhausted(RuntimeError):
    pass


@dataclass
class ReportResult:
    table: pa.Table
    rows: int
    subject_to_thresholding: bool
    sampled: bool


def strip_query(value: str) -> str:
    for sep in ("?", "#"):
        i = value.find(sep)
        if i >= 0:
            value = value[:i]
    return value


def parse_date(value: str) -> dt.date:
    return dt.datetime.strptime(value, "%Y%m%d").date()


class GA4:
    def __init__(self, client: BetaAnalyticsDataClient | None = None):
        self.client = client or BetaAnalyticsDataClient()

    def run(
        self,
        property_id: str,
        report: Report,
        start: dt.date,
        end: dt.date,
        loaded_at: dt.datetime,
    ) -> ReportResult:
        rows: list[list] = []
        metric_types: list[int] | None = None
        thresholded = sampled = False
        offset = 0
        while True:
            request = RunReportRequest(
                property=f"properties/{property_id}",
                dimensions=[Dimension(name=d) for d in report.dimensions],
                metrics=[Metric(name=m) for m in report.metrics],
                date_ranges=[DateRange(start_date=start.isoformat(), end_date=end.isoformat())],
                limit=PAGE_SIZE,
                offset=offset,
                keep_empty_rows=False,
                return_property_quota=True,
            )
            response = self._call(request)
            if metric_types is None:
                metric_types = [h.type_ for h in response.metric_headers]
            meta = response.metadata
            thresholded |= bool(meta.subject_to_thresholding)
            sampled |= bool(meta.sampling_metadatas)
            for r in response.rows:
                rows.append(
                    [d.value for d in r.dimension_values] + [m.value for m in r.metric_values]
                )
            offset += len(response.rows)
            self._check_quota(response.property_quota, property_id)
            if not response.rows or offset >= response.row_count:
                break
        table = to_arrow(property_id, report, rows, metric_types or [], thresholded, sampled, loaded_at)
        return ReportResult(table, len(rows), thresholded, sampled)

    def _call(self, request: RunReportRequest):
        for attempt in range(6):
            try:
                return self.client.run_report(request=request, timeout=300)
            except gexc.ResourceExhausted as e:
                # Hourly or concurrency limits reset on their own; the daily
                # one is caught by _check_quota before it is reached.
                wait = min(HOURLY_WAIT_SECONDS, 60 * 2**attempt)
                log.warning("quota exceeded (%s), retrying in %ss", e.message, wait)
                time.sleep(wait)
            except (gexc.ServiceUnavailable, gexc.InternalServerError, gexc.DeadlineExceeded) as e:
                wait = 10 * 2**attempt
                log.warning("GA4 API error (%s), retrying in %ss", e, wait)
                time.sleep(wait)
        return self.client.run_report(request=request, timeout=300)

    def _check_quota(self, quota, property_id: str) -> None:
        # An unset PropertyQuota reads as all zeros.
        day, hour = quota.tokens_per_day, quota.tokens_per_hour
        if day.consumed + day.remaining and day.remaining < MIN_DAILY_TOKENS:
            raise QuotaExhausted(
                f"property {property_id}: {day.remaining} daily tokens left; resume tomorrow"
            )
        if hour.consumed + hour.remaining and hour.remaining < MIN_HOURLY_TOKENS:
            log.info(
                "property %s: %s hourly tokens left, waiting %ss",
                property_id, hour.remaining, HOURLY_WAIT_SECONDS,
            )
            time.sleep(HOURLY_WAIT_SECONDS)


def schema_for(report: Report, metric_types: list[int]) -> pa.Schema:
    fields = [pa.field("property_id", pa.string(), nullable=False)]
    for d in report.dimensions:
        if d == "date":
            fields.append(pa.field("date", pa.date32(), nullable=False))
        else:
            fields.append(pa.field(snake(d), pa.string()))
    for m, t in zip(report.metrics, metric_types or [None] * len(report.metrics)):
        fields.append(pa.field(snake(m), pa.int64() if t in INTEGER_TYPES else pa.float64()))
    fields += [
        pa.field("subject_to_thresholding", pa.bool_()),
        pa.field("sampled", pa.bool_()),
        pa.field("loaded_at", pa.timestamp("us", tz="UTC")),
    ]
    return pa.schema(fields)


def to_arrow(
    property_id: str,
    report: Report,
    rows: list[list[str]],
    metric_types: list[int],
    thresholded: bool,
    sampled: bool,
    loaded_at: dt.datetime,
) -> pa.Table:
    schema = schema_for(report, metric_types)
    ndims = len(report.dimensions)
    columns: dict[str, list] = {f.name: [] for f in schema}
    for row in rows:
        columns["property_id"].append(property_id)
        for name, value in zip(report.dimensions, row[:ndims]):
            if name == "date":
                columns["date"].append(parse_date(value))
            else:
                if name in PATH_DIMENSIONS:
                    value = strip_query(value)
                columns[snake(name)].append(value)
        for name, value in zip(report.metrics, row[ndims:]):
            col = snake(name)
            if schema.field(col).type == pa.int64():
                columns[col].append(int(float(value)))
            else:
                columns[col].append(float(value))
        columns["subject_to_thresholding"].append(thresholded)
        columns["sampled"].append(sampled)
        columns["loaded_at"].append(loaded_at)
    return pa.Table.from_pydict(columns, schema=schema)
