# Connecting DBeaver to local Trino

The local kind platform exposes Trino at `https://trino.data-platform.local`
with **mkcert TLS** and **Keycloak OAuth2**. DBeaver must trust that CA and
must not use username/password against Trino itself.

Assumes you already ran `make kind-up`, `make run`, and applied
`config/samples/dataplatform_v1alpha1_local.yaml`.

## 1. Trust the mkcert CA in DBeaver's JRE

Browsers trust mkcert after `mkcert -install`. DBeaver ships its own JRE and
does not, so connections fail with `PKIX path building failed` until you import
the CA:

```bash
sudo keytool -importcert -noprompt \
  -alias mkcert-rootCA \
  -file "$(mkcert -CAROOT)/rootCA.pem" \
  -keystore /usr/share/dbeaver-ce/jre/lib/security/cacerts \
  -storepass changeit
```

Fully quit and restart DBeaver after importing. Package upgrades of DBeaver-CE
can replace that JRE; re-run the import if SSL errors return.

On macOS/Windows the `cacerts` path differs (look under the DBeaver install for
`jre/lib/security/cacerts`).

## 2. Create the Trino connection

| Setting | Value |
| --- | --- |
| Host | `trino.data-platform.local` |
| Port | `443` |
| Database / catalog | optional (for example `lakekeeper`) |
| Username | leave **blank**, or use the OIDC `sub` (`00000000-0000-4000-8000-000000000001` for local admin). Do **not** use `admin` — Trino's principal is the `sub` claim, and a mismatched Username triggers `cannot impersonate user admin`. |
| Password | leave **empty** |
| SSL | enabled |

Under **Driver properties**:

| Property | Value |
| --- | --- |
| `SSL` | `true` |
| `externalAuthentication` | `true` |
| `externalAuthenticationRedirectHandlers` | `SYSTEM_OPEN` (or `SWT` if the OS browser does not open) |

Test the connection. A browser window should open to Keycloak. Sign in as
`admin` with the password from:

```bash
kubectl --context kind-data-platform-dev -n keycloak \
  get secret keycloak-admin -o jsonpath='{.data.password}' | base64 -d; echo
```

Trino is configured with `http-server.authentication.type=oauth2` only. Putting
the Keycloak password in DBeaver's password field returns
`Authentication failed: Unauthorized`.

## 3. If the browser redirect fails

Fetch an access token and set the driver property `accessToken` to its value
(leave password empty; you can clear `externalAuthentication`):

```bash
PASS=$(kubectl --context kind-data-platform-dev -n keycloak \
  get secret keycloak-admin -o jsonpath='{.data.password}' | base64 -d)

curl -sS -X POST \
  'https://keycloak.data-platform.local/realms/dataplatform/protocol/openid-connect/token' \
  -d grant_type=password \
  -d client_id=lakekeeper \
  -d username=admin \
  -d "password=${PASS}" | jq -r .access_token
```

Tokens expire; prefer the browser OAuth flow for day-to-day use.
