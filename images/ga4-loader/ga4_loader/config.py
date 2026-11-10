"""Report definitions and their validation.

Each report is one GA4 Data API runReport request shape and one Iceberg
table. The table keeps every property side by side (column property_id), so
analysts query one table across properties.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field

import yaml

# Data API limits per request.
MAX_DIMENSIONS = 9
MAX_METRICS = 10

# Dimensions whose values carry the query string (emails, tokens and order
# ids end up there). The query-free variants (pagePath, landingPage) are
# allowed instead.
FORBIDDEN_DIMENSIONS = frozenset(
    {
        "pagePathPlusQueryString",
        "landingPagePlusQueryString",
        "pageLocation",
        "fullPageUrl",
        "pageReferrer",
    }
)
# User-scoped custom dimensions hold whatever the site sets on the user, which
# is where emails and customer ids get stored.
FORBIDDEN_DIMENSION_PREFIXES = ("customUser:",)

# Dimensions that are paths: any query string or fragment that still shows
# up is cut off before the row is written.
PATH_DIMENSIONS = frozenset({"pagePath", "landingPage", "unifiedPagePathScreen"})

_NAME_RE = re.compile(r"^[a-z][a-z0-9_]*$")


class ConfigError(ValueError):
    pass


@dataclass(frozen=True)
class Report:
    name: str
    dimensions: tuple[str, ...]
    metrics: tuple[str, ...]
    description: str = ""


@dataclass(frozen=True)
class Property:
    id: str
    name: str = ""


@dataclass
class Config:
    reports: list[Report] = field(default_factory=list)
    properties: list[Property] = field(default_factory=list)


def snake(name: str) -> str:
    """GA4 API name to an Iceberg column name: sessionSource -> session_source."""
    name = name.replace(":", "_")
    name = re.sub(r"(?<=[a-z0-9])(?=[A-Z])", "_", name)
    return re.sub(r"[^a-z0-9_]", "_", name.lower())


def parse_reports(raw: dict) -> list[Report]:
    items = (raw or {}).get("reports")
    if not items:
        raise ConfigError("no reports configured")
    reports, seen = [], set()
    for item in items:
        name = item.get("name", "")
        if not _NAME_RE.match(name):
            raise ConfigError(f"report name {name!r} must be snake_case")
        if name in seen:
            raise ConfigError(f"report {name} is defined twice")
        seen.add(name)
        dims = tuple(item.get("dimensions") or ())
        mets = tuple(item.get("metrics") or ())
        if "date" not in dims:
            raise ConfigError(f"report {name}: dimensions must include date")
        if len(dims) > MAX_DIMENSIONS:
            raise ConfigError(f"report {name}: at most {MAX_DIMENSIONS} dimensions")
        if not mets or len(mets) > MAX_METRICS:
            raise ConfigError(f"report {name}: 1 to {MAX_METRICS} metrics")
        for d in dims:
            if d in FORBIDDEN_DIMENSIONS or d.startswith(FORBIDDEN_DIMENSION_PREFIXES):
                raise ConfigError(f"report {name}: dimension {d} can carry personal data")
        cols = [snake(c) for c in dims + mets]
        if len(set(cols)) != len(cols):
            raise ConfigError(f"report {name}: duplicate column names {cols}")
        reports.append(Report(name, dims, mets, item.get("description", "")))
    return reports


def parse_properties(raw: list) -> list[Property]:
    props, seen = [], set()
    for item in raw or []:
        if isinstance(item, (str, int)):
            item = {"id": item}
        pid = str(item.get("id", "")).strip()
        if pid.startswith("properties/"):
            pid = pid.split("/", 1)[1]
        if not pid.isdigit():
            raise ConfigError(
                f"property id {pid!r} must be the numeric GA4 property id, not a G- measurement id"
            )
        if pid in seen:
            raise ConfigError(f"property {pid} is listed twice")
        seen.add(pid)
        props.append(Property(pid, str(item.get("name", ""))))
    if not props:
        raise ConfigError("no properties configured")
    return props


def load(reports_path: str, properties_path: str) -> Config:
    with open(reports_path) as f:
        reports = parse_reports(yaml.safe_load(f))
    with open(properties_path) as f:
        properties = parse_properties(yaml.safe_load(f))
    return Config(reports=reports, properties=properties)
