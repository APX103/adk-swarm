FROM python:3.11-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY main_agent/requirements.txt ./requirements.txt
RUN pip install --no-cache-dir -r requirements.txt

COPY agent_registry/ .

RUN mkdir -p /app/data

ENV REGISTRY_DB_URL=sqlite:////app/data/registry.db
ENV REGISTRY_PROBE_INTERVAL=60

CMD ["python", "server.py"]
