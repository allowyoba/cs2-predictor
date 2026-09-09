const parserOpts = {
    headerPattern: /^(\w*)(?:\((.*)\))?!?: (.*)$/,
    breakingHeaderPattern: /^(\w*)(?:\((.*)\))?!: (.*)$/,
    headerCorrespondence: ["type", "scope", "subject"],
    noteKeywords: ["BREAKING CHANGE", "BREAKING CHANGES"],
};

export default {
    branches: ["main"],
    tagFormat: "v${version}",
    plugins: [
        ["@semantic-release/commit-analyzer", {
            // No catch-all: a merge whose commits are only docs/chore/ci/
            // build/test/style/refactor releases nothing at all — that
            // includes Dependabot's own bumps, see dependabot.yml's
            // commit-message.prefix.
            releaseRules: [
                {breaking: true, release: "major"},
                {type: "feat", release: "minor"},
                {type: "fix", release: "patch"},
                {type: "perf", release: "patch"},
                {type: "revert", release: "patch"},
            ],
            parserOpts,
        }],
        ["@semantic-release/release-notes-generator", {parserOpts}],
        ["@semantic-release/github", {
            successComment: false,
            failComment: false,
            releasedLabels: false,
        }],
    ],
};
