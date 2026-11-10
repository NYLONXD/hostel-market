"""Request bodies, validated by Pydantic before any SQL runs. The database
re-checks the important rules with constraints, so these are the friendly first line."""

from datetime import time
from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, EmailStr, Field, StringConstraints, model_validator

UPI_PATTERN = r"^[a-zA-Z0-9._-]{2,255}@[a-zA-Z]{2,64}$"

Name = Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=100)]
UpiId = Annotated[str, StringConstraints(strip_whitespace=True, pattern=UPI_PATTERN, max_length=100)]
Condition = Literal["new", "like_new", "good", "fair", "poor"]
Year = Annotated[int, Field(ge=1, le=6)]
Price = Annotated[int, Field(ge=1, le=1_000_000, description="whole rupees")]


class Strict(BaseModel):
    model_config = ConfigDict(extra="forbid")


# ---- auth / users ------------------------------------------------------------

class RegisterIn(Strict):
    email: EmailStr
    password: Annotated[str, Field(min_length=8, max_length=72)]
    full_name: Name
    block_id: int
    year_of_study: Year
    upi_id: UpiId | None = None


class LoginIn(Strict):
    email: EmailStr
    password: Annotated[str, Field(min_length=1, max_length=200)]


class ProfilePatch(Strict):
    """Only the fields sent are changed; send upi_id: null to remove it."""
    full_name: Name | None = None
    block_id: int | None = None
    year_of_study: Year | None = None
    upi_id: UpiId | None = None

    @model_validator(mode="after")
    def _no_null_required(self) -> "ProfilePatch":
        for f in ("full_name", "block_id", "year_of_study"):
            if f in self.model_fields_set and getattr(self, f) is None:
                raise ValueError(f"{f} cannot be null")
        return self


# ---- listings ------------------------------------------------------------------

def _check_window(available_from: time | None, available_until: time | None) -> None:
    if (available_from is None) != (available_until is None):
        raise ValueError("set both available_from and available_until, or neither")
    if available_from is not None and available_from == available_until:
        raise ValueError("available_from and available_until must differ")


class ListingIn(Strict):
    title: Annotated[str, StringConstraints(strip_whitespace=True, min_length=3, max_length=120)]
    description: Annotated[str, StringConstraints(strip_whitespace=True, max_length=4000)] = ""
    category: str
    condition: Condition
    price: Price
    available_from: time | None = None
    available_until: time | None = None
    allowed_block_id: int | None = None
    allowed_year: Year | None = None

    @model_validator(mode="after")
    def _window(self) -> "ListingIn":
        _check_window(self.available_from, self.available_until)
        return self


class ListingPatch(Strict):
    """Only the fields sent are changed; send null on a rule field to remove that rule."""
    title: Annotated[str, StringConstraints(strip_whitespace=True, min_length=3, max_length=120)] | None = None
    description: Annotated[str, StringConstraints(strip_whitespace=True, max_length=4000)] | None = None
    category: str | None = None
    condition: Condition | None = None
    price: Price | None = None
    available_from: time | None = None
    available_until: time | None = None
    allowed_block_id: int | None = None
    allowed_year: Year | None = None

    @model_validator(mode="after")
    def _no_null_required(self) -> "ListingPatch":
        for f in ("title", "description", "category", "condition", "price"):
            if f in self.model_fields_set and getattr(self, f) is None:
                raise ValueError(f"{f} cannot be null")
        if ("available_from" in self.model_fields_set) != ("available_until" in self.model_fields_set):
            raise ValueError("send available_from and available_until together")
        _check_window(self.available_from, self.available_until)
        return self


# ---- orders ---------------------------------------------------------------------

OrderAction = Literal["accept", "reject", "cancel", "choose_payment", "confirm_payment", "handover", "complete"]


class OrderRequestIn(Strict):
    note: Annotated[str, StringConstraints(strip_whitespace=True, max_length=500)] | None = None


class OrderActionIn(Strict):
    payment_method: Literal["upi", "cash"] | None = None   # for choose_payment
    reason: Annotated[str, StringConstraints(strip_whitespace=True, max_length=200)] | None = None  # reject / cancel


# ---- chat -----------------------------------------------------------------------

class MessageIn(Strict):
    body: Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=2000)]


class ReadIn(Strict):
    up_to_id: Annotated[int, Field(ge=0)]


# ---- admin ----------------------------------------------------------------------

class VerifyIn(Strict):
    verified: bool = True


class RemoveIn(Strict):
    reason: Annotated[str, StringConstraints(strip_whitespace=True, max_length=200)] | None = None


class HostelIn(Strict):
    name: Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=100)]


class BlockIn(Strict):
    name: Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=20, to_upper=True)]


class CategoryIn(Strict):
    slug: Annotated[str, StringConstraints(strip_whitespace=True, pattern=r"^[a-z_]{1,30}$")]
    name: Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=50)]
