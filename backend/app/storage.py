"""Photo storage: Cloudinary when configured, otherwise a local uploads/ folder.

Both store the image and return (public_url, storage_key). The key is what the
database keeps in listing_photos.storage_key so the file can be deleted later.
"""

import io
import logging
import uuid
from pathlib import Path
from urllib.parse import urlparse

from .config import get_settings
from .errors import AppError

log = logging.getLogger("hostel_market")

_LOCAL_PREFIX = "local:"


def detect_image(data: bytes) -> str | None:
    """Return the file extension for JPEG/PNG/WebP data, judged by its magic bytes."""
    if data[:3] == b"\xff\xd8\xff":
        return "jpg"
    if data[:8] == b"\x89PNG\r\n\x1a\n":
        return "png"
    if data[:4] == b"RIFF" and data[8:12] == b"WEBP":
        return "webp"
    return None


def _cloudinary():
    """Configure and return the cloudinary.uploader module, or None if not set up."""
    url = get_settings().cloudinary_url
    if not url:
        return None
    import cloudinary
    import cloudinary.uploader

    u = urlparse(url)
    cloudinary.config(cloud_name=u.hostname, api_key=u.username, api_secret=u.password, secure=True)
    return cloudinary.uploader


def using_cloudinary() -> bool:
    return bool(get_settings().cloudinary_url)


def local_dir() -> Path:
    path = Path(get_settings().upload_dir)
    path.mkdir(parents=True, exist_ok=True)
    return path


def save_photo(data: bytes, ext: str, user_id: int) -> tuple[str, str]:
    s = get_settings()
    uploader = _cloudinary()
    if uploader is not None:
        try:
            res = uploader.upload(
                io.BytesIO(data),
                folder=f"{s.cloudinary_folder}/users/{user_id}",
                resource_type="image",
                allowed_formats=["jpg", "jpeg", "png", "webp"],
            )
        except Exception as exc:  # network or Cloudinary error
            log.exception("cloudinary upload failed")
            raise AppError(502, "upload_failed", "could not upload the photo, try again") from exc
        return res["secure_url"], res["public_id"]

    name = f"{uuid.uuid4().hex}.{ext}"
    (local_dir() / name).write_bytes(data)
    return f"{s.public_base_url.rstrip('/')}/uploads/{name}", _LOCAL_PREFIX + name


def delete_photo(storage_key: str) -> None:
    """Best effort: a file left behind is harmless, so failures are only logged."""
    try:
        if storage_key.startswith(_LOCAL_PREFIX):
            name = Path(storage_key[len(_LOCAL_PREFIX):]).name  # never follow paths
            (local_dir() / name).unlink(missing_ok=True)
            return
        uploader = _cloudinary()
        if uploader is not None:
            uploader.destroy(storage_key, resource_type="image")
    except Exception:
        log.warning("could not delete photo %s", storage_key)
