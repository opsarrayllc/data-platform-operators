# dbt on Trino for data-orchestration-operator Transformations. The operator
# runs it from Argo Workflows; see docs/transformations.md for the contract.
FROM python:3.13-slim

# git clones the project and any git-hosted dbt packages.
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    DBT_SEND_ANONYMOUS_USAGE_STATS=false \
    DBT_USE_COLORS=false \
    DO_NOT_TRACK=1 \
    GIT_TERMINAL_PROMPT=0

WORKDIR /app
COPY requirements.txt .
RUN pip install -r requirements.txt
COPY dbt_runner ./dbt_runner

# The operator runs the pod as 65532 with a read-only root filesystem; /work
# (HOME) and /tmp are writable emptyDirs.
USER 65532:65532
ENTRYPOINT ["python", "-m", "dbt_runner"]
