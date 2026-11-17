"""The lane state ConfigMap, read and written through the Kubernetes API.

The operator creates the ConfigMap empty and gives the runner's
ServiceAccount get and patch on that one object. Each on-change lane keeps
one key, "<lane>.json", holding the snapshot it last processed per table.
"""

from __future__ import annotations

import datetime as dt
import json
import os
from typing import Callable

import requests

SA_DIR = "/var/run/secrets/kubernetes.io/serviceaccount"


class KubeConfigMap:
    def __init__(
        self,
        namespace: str,
        name: str,
        *,
        session: requests.Session | None = None,
        base_url: str | None = None,
        token: Callable[[], str] | None = None,
        verify: str | bool | None = None,
    ):
        host = os.environ.get("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
        port = os.environ.get("KUBERNETES_SERVICE_PORT", "443")
        if ":" in host:  # IPv6 service address
            host = f"[{host}]"
        self.url = f"{base_url or f'https://{host}:{port}'}/api/v1/namespaces/{namespace}/configmaps/{name}"
        self.session = session or requests.Session()
        self.token = token or _sa_token
        self.verify = f"{SA_DIR}/ca.crt" if verify is None else verify

    def _headers(self, extra: dict[str, str] | None = None) -> dict[str, str]:
        return {"Authorization": f"Bearer {self.token()}", **(extra or {})}

    def get(self, key: str) -> str | None:
        r = self.session.get(self.url, headers=self._headers(), verify=self.verify, timeout=30)
        r.raise_for_status()
        return (r.json().get("data") or {}).get(key)

    def set(self, key: str, value: str) -> None:
        body = json.dumps({"data": {key: value}})
        r = self.session.patch(
            self.url,
            data=body,
            headers=self._headers({"Content-Type": "application/merge-patch+json"}),
            verify=self.verify,
            timeout=30,
        )
        r.raise_for_status()


def _sa_token() -> str:
    # Rotated by the kubelet, so read it per request.
    with open(f"{SA_DIR}/token") as f:
        return f.read().strip()


def lane_key(lane: str) -> str:
    return f"{lane}.json"


def decode(raw: str | None) -> dict[str, str]:
    """Table -> snapshot id. A missing or unreadable value means a first run."""
    if not raw:
        return {}
    try:
        value = json.loads(raw)
    except json.JSONDecodeError:
        return {}
    snapshots = value.get("snapshots") if isinstance(value, dict) else None
    if not isinstance(snapshots, dict):
        return {}
    return {str(k): str(v) for k, v in snapshots.items()}


def encode(snapshots: dict[str, str], now: dt.datetime | None = None) -> str:
    now = now or dt.datetime.now(dt.timezone.utc)
    return json.dumps(
        {"snapshots": dict(sorted(snapshots.items())), "processedAt": now.strftime("%Y-%m-%dT%H:%M:%SZ")},
        indent=1,
    )
