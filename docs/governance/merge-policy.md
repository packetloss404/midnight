# Merge Policy

- Merge only through pull requests.
- Require CODEOWNERS review for touched areas.
- Require two approvals for security-sensitive work.
- Use the protected-branch baseline in `docs/governance/branch-protection.json`.
- Keep required status checks empty while GitHub Actions remain manual-only.
- Do not force-push protected branches.
- Release artifacts are built only from protected refs.
- Fork PR workflows must not access secrets or push images.
