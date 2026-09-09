#!/usr/bin/env bash
#
# Applies this project's branch protection and environment rules to a
# GitHub repository. Run once, from a machine where `gh auth status`
# shows admin rights on the repository:
#
#   scripts/configure-repository.sh OWNER/REPO [REVIEWER...]
#
# Each REVIEWER becomes a required reviewer on the "production"
# environment (deploy.yml, webhook.yml) — without one, that environment
# only supplies secrets, it isn't a gate on anything.
set -euo pipefail

repo="${1:-}"
if [[ ! "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
	echo "usage: ${0##*/} OWNER/REPO [REVIEWER...]" >&2
	exit 1
fi
shift

branch="$(gh api "repos/$repo" --jq .default_branch)"

# Squash only, so the default branch stays one commit per pull request
# and the release workflow can read a clean list of Conventional Commit
# subjects. The pull request title becomes the commit subject, which is
# why .github/workflows/ci.yml checks that it parses.
gh api "repos/$repo" --method PATCH --input - >/dev/null <<'JSON'
{
  "allow_squash_merge": true,
  "allow_merge_commit": false,
  "allow_rebase_merge": false,
  "squash_merge_commit_title": "PR_TITLE",
  "squash_merge_commit_message": "PR_BODY",
  "delete_branch_on_merge": true
}
JSON

# Contexts are job names from ci.yml plus preflight.yml's own commit
# status; rename one there and here together.
gh api "repos/$repo/branches/$branch/protection" --method PUT --input - >/dev/null <<'JSON'
{
  "required_status_checks": {
    "strict": true,
    "contexts": ["Quality checks", "Image build", "Deployment automation checks", "Deployment preflight"]
  },
  "enforce_admins": true,
  "required_pull_request_reviews": {
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": true,
    "required_approving_review_count": 1,
    "require_last_push_approval": true
  },
  "restrictions": null,
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_conversation_resolution": true
}
JSON

echo "Protected default branch of $repo: $branch"

reviewers_json="[]"
for reviewer in "$@"; do
	id="$(gh api "users/$reviewer" --jq .id)"
	reviewers_json="$(jq --argjson id "$id" '. + [{"type": "User", "id": $id}]' <<<"$reviewers_json")"
done

gh api "repos/$repo/environments/production" --method PUT --input - >/dev/null <<JSON
{
  "wait_timer": 0,
  "prevent_self_review": true,
  "reviewers": $reviewers_json,
  "deployment_branch_policy": {"protected_branches": true, "custom_branch_policies": false}
}
JSON

if [[ "$#" -eq 0 ]]; then
	echo "Configured environment 'production' on $repo (no required reviewers passed — pass usernames to require one)."
else
	echo "Configured environment 'production' on $repo, required reviewers: $*"
fi
