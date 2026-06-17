import os
from fastapi import FastAPI
from fastapi.staticfiles import StaticFiles

ARTIFACTS_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "artifacts")
os.makedirs(ARTIFACTS_DIR, exist_ok=True)

app = FastAPI()
app.mount("/artifacts", StaticFiles(directory=ARTIFACTS_DIR), name="artifacts")


def run_file_server(port: int = 8080):
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=port, log_level="warning")
