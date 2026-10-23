# Federated CI Identity Foundations

`internal/auth.FederatedCredentialConfig` defines the local contract for CI-issued OIDC tokens that can exchange into a Midnight service-account session later.

Validated fields:

- `provider`: one of `jenkins`, `azure_devops`, `bitbucket`, `github_actions`, or `gitlab_ci`.
- `issuer_url`: absolute HTTPS URL with no query or fragment.
- `subject`: exact CI token subject expected for the repository, branch, job, or service connection.
- `audience`: exact audience expected by Midnight, for example `api://midnight/workload-identity`.
- `workspace_id` and `service_account_id`: target Midnight identity binding.

`Matches` currently performs exact `iss`, `sub`, and `aud` comparison after config validation. JWT parsing, JWKS discovery, token exchange, audit emission, and service-account session minting remain future work.
