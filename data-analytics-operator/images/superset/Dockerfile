# Superset with OAuth + Postgres + Trino extras baked in.
# The stock apache/superset image is lean and does not include authlib,
# psycopg2, redis, or the Trino SQLAlchemy dialect. Installing them at pod
# start requires PyPI egress, which kind/airgapped clusters often lack.
ARG SUPERSET_BASE=apache/superset:5.0.0
FROM ${SUPERSET_BASE}

USER root
# The image venv has no pip module; install packages into its site-packages.
RUN /usr/local/bin/pip install --no-cache-dir \
      --target=/app/.venv/lib/python3.10/site-packages \
      'authlib>=1.0.0' \
      'psycopg2-binary>=2.9.0' \
      'redis>=4.0.0' \
      'sqlalchemy-trino>=0.5.0'
USER superset
