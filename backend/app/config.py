"""Application settings, read from environment variables (and backend/.env)."""

from functools import lru_cache

from pydantic import model_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", env_file_encoding="utf-8", extra="ignore")

    app_env: str = "development"  # development | production | test

    # PostgreSQL, e.g. postgresql://user:pass@host/db?sslmode=require (Neon, Supabase, Render ...)
    database_url: str
    db_pool_max: int = 10

    # Optional Redis cache (feed pages, hostel/category lists). Empty = no cache.
    redis_url: str = ""
    feed_cache_seconds: int = 60

    jwt_secret: str
    jwt_expire_minutes: int = 60 * 24 * 7
    bcrypt_rounds: int = 12

    # Sign-ups with these email domains are verified automatically (comma separated).
    college_email_domains: str = ""

    cors_origins: str = "http://localhost:3000"
    order_request_ttl_hours: int = 48
    expire_job_seconds: int = 60

    # Photos: Cloudinary when CLOUDINARY_URL is set, otherwise the local uploads folder.
    cloudinary_url: str = ""          # cloudinary://<api_key>:<api_secret>@<cloud_name>
    cloudinary_folder: str = "hostel_market"
    upload_dir: str = "uploads"
    public_base_url: str = "http://localhost:8000"
    max_upload_mb: int = 5

    @model_validator(mode="after")
    def _check_secret(self) -> "Settings":
        if self.app_env == "production" and len(self.jwt_secret) < 32:
            raise ValueError("JWT_SECRET must be at least 32 characters in production")
        return self

    @property
    def college_domains(self) -> list[str]:
        return [d.strip().lower().lstrip("@") for d in self.college_email_domains.split(",") if d.strip()]

    @property
    def cors_origin_list(self) -> list[str]:
        return [o.strip() for o in self.cors_origins.split(",") if o.strip()]


@lru_cache
def get_settings() -> Settings:
    return Settings()  # type: ignore[call-arg]
