"""Promote an existing account to admin (and verify it).

    python -m scripts.make_admin someone@college.edu
"""

import sys

import psycopg

from app.config import get_settings


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__)
        return 1
    email = sys.argv[1].strip().lower()
    with psycopg.connect(get_settings().database_url, prepare_threshold=None, autocommit=True) as conn:
        cur = conn.execute(
            "UPDATE users SET role = 'admin', is_verified = true, verified_at = COALESCE(verified_at, now()) "
            "WHERE email = %s",
            (email,),
        )
    if cur.rowcount == 0:
        print(f"no user with email {email}")
        return 1
    print(f"{email} is now an admin")
    return 0


if __name__ == "__main__":
    sys.exit(main())
