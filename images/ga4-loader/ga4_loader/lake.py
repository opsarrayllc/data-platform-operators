"""Writes report tables into the LakeKeeper warehouse through pyiceberg."""

from __future__ import annotations

import datetime as dt
import logging
import os

import pyarrow as pa
import requests
from pyiceberg.catalog import load_catalog
from pyiceberg.catalog.rest import RestCatalog
from pyiceberg.exceptions import NamespaceAlreadyExistsError, NoSuchTableError
from pyiceberg.expressions import And, EqualTo, GreaterThanOrEqual, LessThanOrEqual
from pyiceberg.table import Table
from pyiceberg.transforms import IdentityTransform, YearTransform

log = logging.getLogger(__name__)


class Lake:
    """One catalog session per write.

    The projected ServiceAccount token rotates hourly, and a backfill can run
    longer, so the token file is re-read for every catalog it opens.
    """

    def __init__(self, uri: str, warehouse: str, namespace: str, token_file: str, region: str):
        self.uri = uri.rstrip("/")
        self.warehouse = warehouse
        self.namespace = namespace
        self.token_file = token_file
        self.region = region

    def _token(self) -> str:
        with open(self.token_file) as f:
            return f.read().strip()

    def catalog(self) -> RestCatalog:
        return load_catalog(
            "lake",
            type="rest",
            uri=self.uri,
            warehouse=self.warehouse,
            token=self._token(),
            # LakeKeeper vends table-scoped S3 credentials, as it does for
            # Trino and Debezium, so the pod needs no AWS identity.
            **{
                "header.X-Iceberg-Access-Delegation": "vended-credentials",
                "s3.region": self.region,
            },
        )

    def register(self) -> None:
        """Provision this ServiceAccount as a LakeKeeper user.

        LakeKeeper only lists principals it has seen, so the admin can grant
        the loader access only after this has run once.
        """
        base = self.uri.removesuffix("/catalog")
        resp = requests.post(
            f"{base}/management/v1/user",
            json={"name": os.environ.get("LAKEKEEPER_USER_NAME", "ga4-loader"),
                  "user-type": "application", "update-if-exists": True},
            headers={"Authorization": f"Bearer {self._token()}"},
            timeout=30,
        )
        if resp.status_code >= 400:
            log.warning("LakeKeeper user registration returned %s: %s", resp.status_code, resp.text[:300])

    def ensure_namespace(self) -> None:
        cat = self.catalog()
        try:
            cat.create_namespace(self.namespace)
            log.info("created namespace %s", self.namespace)
        except NamespaceAlreadyExistsError:
            pass

    def _table(self, cat: RestCatalog, name: str, schema: pa.Schema) -> Table:
        ident = (self.namespace, name)
        try:
            table = cat.load_table(ident)
        except NoSuchTableError:
            table = cat.create_table(ident, schema=schema)
            # Partition after creation, by column name: pyiceberg assigns the
            # field ids when it converts the Arrow schema. Report tables are
            # small aggregates, so a year per property is plenty.
            with table.update_spec() as spec:
                spec.add_field("property_id", IdentityTransform(), "property_id")
                spec.add_field("date", YearTransform(), "date_year")
            log.info("created table %s.%s", *ident)
            return cat.load_table(ident)
        # A report that gained a dimension or metric in the config gains the
        # column; existing rows read it as null.
        with table.update_schema() as update:
            update.union_by_name(schema)
        return cat.load_table(ident)

    def replace(self, name: str, property_id: str, start: dt.date, end: dt.date, data: pa.Table) -> None:
        """Replace one property's rows for [start, end] with data, atomically."""
        cat = self.catalog()
        rows = And(
            EqualTo("property_id", property_id),
            And(GreaterThanOrEqual("date", start.isoformat()), LessThanOrEqual("date", end.isoformat())),
        )
        try:
            table = self._table(cat, name, data.schema) if data.num_rows else cat.load_table((self.namespace, name))
        except NoSuchTableError:
            return  # Nothing loaded before and nothing to load now.
        if data.num_rows:
            table.overwrite(data, overwrite_filter=rows)
        else:
            table.delete(delete_filter=rows)

