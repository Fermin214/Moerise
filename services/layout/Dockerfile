FROM python:3.12-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 \
    NUMBA_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2 OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 \
    NUMBA_CACHE_DIR=/data/cache MOERISE_LAYOUT_STATE=/data
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt \
    && useradd --uid 10001 --create-home layout \
    && mkdir -p /data/cache /data/models /data/jobs && chown -R 10001:10001 /data
COPY server.py semantic.py ./
USER 10001:10001
EXPOSE 8766
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s \
    CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8766/health', timeout=2)"]
CMD ["python", "server.py", "serve", "--host", "0.0.0.0"]
