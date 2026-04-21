#!/usr/bin/env python3
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

from playground_service import main

if __name__ == "__main__":
    raise SystemExit(main([sys.argv[0], "worker"]))
