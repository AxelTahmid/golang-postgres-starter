-- name: GetUser :one
SELECT
    *
FROM
    auth_users
WHERE
    email = LOWER(@Email);

-- name: CreateAdminUser :one
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

-- name: CreateCustomerUser :one
WITH
    new_customer AS (
        INSERT INTO
            customers (tenant_id, first_name, last_name)
        VALUES
            (
                CURRENT_SETTING('app.current_tenant')::INT, -- Current tenant ID
                @FirstName,
                @LastName
            )
        RETURNING
            id AS customer_id
    )
INSERT INTO
    auth_users (
        email,
        phone,
        password,
        role,
        tenant_id,
        customer_id
    )
SELECT
    LOWER(@Email),
    @Phone,
    @Password,
    'customer'::role_type,
    CURRENT_SETTING('app.current_tenant')::INT, -- Tenant ID
    customer_id
FROM
    new_customer
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