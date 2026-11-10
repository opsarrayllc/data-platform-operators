# JumpCloud and other external identity providers

Embedded Keycloak is for local development. In a shared environment, people sign in through your own OIDC provider. This page uses JumpCloud as the example, because it has the strictest limits, but the same settings apply to any provider.

There are two kinds of sign-in:

- **People** sign in through the browser to the LakeKeeper UI, Trino, Superset, and Argo. They use JumpCloud directly, with the authorization-code flow.
- **Machines** call LakeKeeper: the operator, Trino's Iceberg catalog, the OPA bridge, and Debezium Server when `spec.debezium` has sources. They use the OAuth2 client-credentials grant, which JumpCloud doesn't offer. Set `spec.auth.serviceAccountTokens` so they use Kubernetes ServiceAccount tokens instead.

## JumpCloud facts that matter

From `https://oauth.id.jumpcloud.com/.well-known/openid-configuration`:

- The issuer is `https://oauth.id.jumpcloud.com/`, **with** the trailing slash. Copy it exactly. Tokens are checked against this string.
- The only grants are `authorization_code` and `refresh_token`, so there's no client-credentials grant.
- The scopes are `openid`, `email`, `profile`, and `offline_access`. There are no custom scopes, so set `scope: openid`.
- Group membership is sent only if you turn on the application's group attribute. Name it `groups`, or set `groupsClaim` to the name you chose.

## JumpCloud applications

Create one OIDC application per client:

| Application | Used by | Redirect URIs |
| --- | --- | --- |
| `lakekeeper` | LakeKeeper UI (public client, PKCE) | `https://lakekeeper.example.com/ui/callback` |
| `trino` | Trino web UI and clients, Superset's per-user Trino login | `https://trino.example.com/oauth2/callback`, `https://superset.example.com/api/v1/database/oauth2/` |
| `superset` | Superset login | `https://superset.example.com/oauth-authorized/oidc` |
| `argo` | Argo login | `https://argo.example.com/oauth2/callback` |

Turn on the group attribute (`groups`) on the `trino`, `superset`, and `argo` applications. Create the JumpCloud user groups `platform-admins`, `data-engineers`, and `analysts`, and bind them to the applications. Superset and Argo map those group names to roles.

Put the client secrets in one Secret:

```bash
kubectl -n lakekeeper create secret generic jumpcloud-oidc \
  --from-literal=clientID=<lakekeeper client id> \
  --from-literal=clientSecret=<lakekeeper client secret> \
  --from-literal=trinoClientID=<trino client id> \
  --from-literal=trinoClientSecret=<trino client secret> \
  --from-literal=supersetClientID=<superset client id> \
  --from-literal=supersetClientSecret=<superset client secret> \
  --from-literal=argoClientID=<argo client id> \
  --from-literal=argoClientSecret=<argo client secret>
```

## DataLake

```yaml
spec:
  auth:
    embedded: false
    oidc:
      issuer: https://oauth.id.jumpcloud.com/
      clientID: <lakekeeper client id>
      audience: <lakekeeper client id>
      scope: openid
      # JumpCloud access tokens are opaque; LakeKeeper only accepts JWTs.
      uiTokenType: id_token
      subjectClaim: sub
      credentialsSecretRef:
        name: jumpcloud-oidc
        namespace: lakekeeper
        clientIDKey: clientID
        trinoClientIDKey: trinoClientID
        trinoClientSecretKey: trinoClientSecret
    serviceAccountTokens: {}
    groupSync:
      jumpcloud:
        apiKeySecretRef:
          name: jumpcloud-api
          namespace: lakekeeper
        subjectAttribute: id   # the JumpCloud attribute that equals the token's sub
  trino:
    publicURL: https://trino.example.com
  lakekeeper:
    publicURL: https://lakekeeper.example.com
```

With `serviceAccountTokens`, the operator does the following:

- **LakeKeeper:** turns on Kubernetes authentication, with audience `lakekeeper`, and binds the `lakekeeper` ServiceAccount to `system:auth-delegator` so it can call TokenReview. LakeKeeper user ids for machines become `kubernetes~system:serviceaccount:<namespace>:<name>`.
- **The operator itself:** mints 10-minute tokens for the `lakekeeper-operator` ServiceAccount. That account bootstraps LakeKeeper, so it becomes the server admin.
- **Trino and OPA:** run as the `trino` and `opa` ServiceAccounts, each with a small busybox sidecar. The sidecar serves the pod's projected token on `http://127.0.0.1:8099/cgi-bin/token`, in the shape of an OAuth2 token response. The kubelet rotates the token file, and the sidecar reports a 5-minute expiry, so both clients pick up new tokens without a restart.
- **Debezium Server** (when `spec.debezium` has sources): runs as the `debezium` ServiceAccount in the Debezium namespace, with the same sidecar. The operator grants `kubernetes~system:serviceaccount:debezium:debezium` `project_admin`. See [`debezium.md`](debezium.md).
- **Grants:** gives Trino `project_admin` and OPA `security_admin`, and registers both as trusted Trino engines.
- **Other workloads** (`serviceAccountTokens.grants`): batch jobs such as Argo Workflows run as their own ServiceAccounts. For each grant, the operator registers `kubernetes~system:serviceaccount:<namespace>:<name>` as a LakeKeeper application user, creates the listed Iceberg namespaces when they're missing, and grants that user `ownership` on each one, plus `describe` on the warehouse. Without `describe`, every request's warehouse check makes OpenFGA walk all namespaces and tables in the warehouse, and those checks time out. `describe` shows names and schemas, not data. The pod authenticates with a projected token for audience `lakekeeper`. Removing a grant doesn't revoke it.

  ```yaml
  serviceAccountTokens:
    grants:
      - namespace: argo-workflows
        name: ga4-loader
        icebergNamespaces: [google_analytics]
  ```

### LakeKeeper UI sign-in

JumpCloud issues opaque access tokens, and publishes no introspection endpoint. LakeKeeper validates bearer tokens as JWTs, so it rejects them with `No authenticator can handle the provided token`. With `uiTokenType: id_token`, the UI sends the ID token instead. That token is a JWT signed by the issuer, and its `aud` is the lakekeeper client ID, so `audience` must be that client ID.

Iceberg clients that call LakeKeeper directly, such as PyIceberg or Spark, also need a JWT. JumpCloud's access token won't work for them; query through Trino instead.

### Group sync

`groupSync.jumpcloud` fills the LakeKeeper roles `platform-admins`, `data-engineers`, and `analysts` from JumpCloud user groups of the same names. To use other group names, map them with `groupNames`, for example `groupNames: {platform-admins: "Data Platform Admins"}`. The operator syncs every minute:

- It reads users from `/api/systemusers` and group members from `/api/v2/usergroups/{id}/members`, with the API key from `apiKeySecretRef` (key `apiKey`). JumpCloud API keys carry every permission of the admin who created them, so make the key with a read-only admin.
- Each active member becomes the LakeKeeper user `oidc~<subjectAttribute>`. This must equal what LakeKeeper reads from `subjectClaim`, or the role assignments won't match the people who sign in. Decode a real ID token to check `sub`.
- Suspended, staged, and not-yet-activated users are skipped.
- Role membership mirrors JumpCloud. `oidc~` users who are no longer in the group are removed, including anyone added to the role by hand. ServiceAccounts and role-to-role assignments are left alone.
- If JumpCloud is unreachable, rejects the key, or a group is missing, nothing changes, and the error appears on the DataLake's `WarehouseReady` condition.

## Analytics (Superset)

```yaml
spec:
  publicURL: https://superset.example.com
  auth:
    enabled: true
    oidc:
      issuer: https://oauth.id.jumpcloud.com/
      groupsClaim: groups
      credentialsSecretRef:
        name: jumpcloud-oidc
        namespace: lakekeeper
```

For a non-Keycloak issuer, the analytics operator reads the login, token, JWKS, and userinfo URLs from the issuer's discovery document. It also checks that the document's issuer matches `issuer` exactly. Superset signs users in with a generic OIDC provider:

- The Superset username is the OIDC subject, the same principal Trino sees.
- Groups come from `groupsClaim`, in the ID token or userinfo.

You can override each endpoint with `authorizationEndpoint`, `tokenEndpoint`, `jwksURI`, or `userinfoEndpoint`.

## Orchestration (Argo)

```yaml
spec:
  publicURL: https://argo.example.com
  auth:
    enabled: true
    oidc:
      publicIssuer: https://oauth.id.jumpcloud.com/
      groupsClaim: groups
      credentialsSecretRef:
        name: jumpcloud-oidc
        namespace: lakekeeper
```

### A private CA in front of the issuer

Argo fetches the issuer's discovery document itself. If the issuer sits behind a private CA, put the root in a ConfigMap under the key `ca.crt`, in the Argo namespace, and set `spec.auth.oidc.caConfigMapName`. The local kind setup does this for the mkcert root (`data-platform-ca`). JumpCloud uses a public CA, so it needs no ConfigMap.

## Known gaps

- **Verified in kind, not yet in EKS.** `serviceAccountTokens` was run on a kind cluster with embedded Keycloak and against the JumpCloud issuer:
  - LakeKeeper accepted the operator, Trino, and OPA tokens.
  - Sample data seeded, and OPA allowed and denied queries correctly.
  - Group sync ran on the operator token.
  - Queries kept working after the sidecar's token expiry.
  - Every login redirected to JumpCloud.

  A real JumpCloud sign-in hasn't been tried.
- **New installs only.** LakeKeeper makes whoever bootstraps it the server admin. If the `operator` OIDC client already bootstrapped LakeKeeper, the `lakekeeper-operator` ServiceAccount has no rights there.
- **Group sync against a real JumpCloud tenant.** The client is tested against a fake JumpCloud API built from JumpCloud's documented endpoints, not yet against a real tenant.
- **Row-filter group tuples are add-only.** Group sync writes `group:<name>#member` tuples to the row-filter store but doesn't remove them. A person removed from a group loses the role, and with it table access, but keeps the stale tuple.
- **Superset per-user Trino queries.** Superset sends the user's own JumpCloud token to Trino. Trino validates opaque tokens through the userinfo endpoint.
