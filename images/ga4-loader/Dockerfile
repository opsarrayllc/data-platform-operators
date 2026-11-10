# GA4 Data API → Iceberg loader. Run by Argo Workflows (see README.md).
FROM python:3.13-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

WORKDIR /app
COPY requirements.txt .
RUN pip install -r requirements.txt
COPY ga4_loader ./ga4_loader

# Any non-root uid works; pyiceberg writes nothing to disk.
USER 65532:65532
ENTRYPOINT ["python", "-m", "ga4_loader"]
