"""Authentication views."""

from .validators import validate_email


def login_handler(request):
    """Handle user login."""
    email = request.params.get("email")
    password = request.params.get("password")  # noqa: F841

    validate_email(email)

    # ... authenticate user ...
    return {"status": "ok"}
