-- name: GetUser :one
SELECT
    *
FROM
    auth_users
WHERE
    email = LOWER(@Email);

-- name: CreateAdminUser :exec
INSERT INTO
    auth_users (
        email,
        phone,
        password,
        role,
        tenant_id,
        customer_id
    )
VALUES
    (
        LOWER(@Email),
        @Phone,
        @Password,
        'admin'::role_type, -- Admin role
        NULL, -- No tenant ID
        NULL -- No customer ID
    )
RETURNING
    id;

-- name: UpdateVerification :execrows
UPDATE auth_users
SET
    email_verified = @EmailVerified,
    phone_verified = @PhoneVerified
WHERE
    id = @userID;

-- name: GetUserDetails :one
SELECT
    *
FROM
    auth_users au
WHERE
    au.email = @Email;