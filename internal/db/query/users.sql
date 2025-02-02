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

-- name: CreateTenantUser :one
WITH
    new_tenant AS (
        INSERT INTO
            tenants (full_name, company_name, domain)
        VALUES
            (@FullName, @CompanyName, @Domain)
        RETURNING
            id AS tenant_id
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
    'tenant'::role_type,
    tenant_id,
    NULL -- No customer ID for tenant
FROM
    new_tenant
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
    -- auth_user columns
    au.*,
    -- tenants cloumns
    t.full_name AS tenant_name,
    t.company_name AS tenant_company_name,
    t.domain AS tenant_domain,
    t.status AS tenant_status,
    t.store_settings AS tenant_store_settings,
    t.payment_config AS tenant_payment_config,
    t.admin_settings AS tenant_admin_settings,
    t.suspended_at AS tenant_suspended_at,
    t.created_at AS tenant_created_at,
    t.updated_at AS tenant_updated_at,
    -- customer columns
    c.first_name AS customer_first_name,
    c.last_name AS customer_last_name,
    c.address_book AS customer_address_book,
    c.settings AS customer_settings,
    c.created_at AS customer_created_at
FROM
    auth_users au
    LEFT JOIN tenants t ON au.tenant_id = t.id
    LEFT JOIN customers c ON au.customer_id = c.id
WHERE
    au.email = @Email
    AND (
        (
            au.role = 'admin'
            AND au.tenant_id IS NULL
            AND au.customer_id IS NULL
        )
        OR (
            au.role = 'tenant'
            AND au.tenant_id IS NOT NULL
            AND au.customer_id IS NULL
        )
        OR (
            au.role = 'customer'
            AND au.tenant_id IS NOT NULL
            AND au.customer_id IS NOT NULL
        )
    );