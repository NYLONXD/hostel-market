"""Create the database objects (and optionally sample data).

    python -m scripts.init_db            # tables, functions, triggers, views
    python -m scripts.init_db --seed     # ... plus the demo data in 04_seed.sql
    python -m scripts.init_db --reset --seed   # DROP everything first (asks to confirm)

Uses DATABASE_URL from backend/.env (or the environment).
"""

import argparse
import sys
from pathlib import Path

import psycopg

from app.config import get_settings

SQL_DIR = Path(__file__).resolve().parent.parent / "database"


def run_files(conn: psycopg.Connection, names: list[str]) -> None:
    for name in names:
        print(f"  running {name}")
        conn.execute((SQL_DIR / name).read_text(encoding="utf-8"))


def apply(url: str, *, reset: bool, seed: bool) -> None:
    files = ["01_schema.sql", "02_functions_triggers.sql", "03_views.sql"]
    if seed:
        files.append("04_seed.sql")
    if reset:
        files.insert(0, "00_reset.sql")
    # One transaction: if any file fails, nothing is left half-created.
    with psycopg.connect(url, prepare_threshold=None) as conn:
        with conn.transaction():
            run_files(conn, files)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--reset", action="store_true", help="drop all Hostel Market tables first (deletes data)")
    parser.add_argument("--seed", action="store_true", help="load the demo data")
    parser.add_argument("--yes", action="store_true", help="don't ask before --reset")
    args = parser.parse_args()

    url = get_settings().database_url
    if args.reset and not args.yes:
        answer = input("This deletes ALL Hostel Market data in the database. Type 'reset' to continue: ")
        if answer.strip() != "reset":
            print("aborted")
            return 1
    apply(url, reset=args.reset, seed=args.seed)
    print("done")
    return 0


if __name__ == "__main__":
    sys.exit(main())
