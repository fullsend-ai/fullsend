"""Input validators for authentication."""

import re


def validate_email(email: str) -> None:
    """Validate an email address.

    BUG: This regex rejects '+' in the local part, which is valid per RFC 5321.
    """
    pattern = r"^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$"
    if not re.match(pattern, email):
        raise ValueError("invalid email format")
