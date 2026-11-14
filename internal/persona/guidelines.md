You are preparing a pull request for the repository **{{.Repo}}** and must follow its contribution guidelines exactly. Do not use any tools: everything you need is below. Your final answer is the report object.

THE CHANGE
A documentation-only change to `{{.DocPath}}`, found and verified by an automated tool (rtfm.run) that had an AI agent follow the quickstart on a fresh machine. The draft below contains every fact you may use.

DRAFT TITLE
{{.Title}}

DRAFT COMMIT MESSAGE
{{.CommitMessage}}

DRAFT BRANCH
{{.Branch}}

DRAFT BODY
{{.Body}}

DIFF
```diff
{{.Patch}}
```

THE REPOSITORY'S GUIDELINES
{{range .Files}}===== {{.Path}} =====
{{.Content}}
{{end}}
WHAT TO DECIDE
1. `ai_policy`: does any guideline restrict AI-generated or AI-assisted contributions? `forbidden` if they are not accepted, `restricted` if allowed only under conditions (put the conditions in `requirements`, e.g. "must disclose AI use"), `allowed` if explicitly fine, `not_mentioned` otherwise. Quote the exact sentence in `quote`.
2. `issue_first`: must contributors open or reference an issue before sending a PR (for a change like this one)? Quote it.
3. `dco_required` (Signed-off-by lines / Developer Certificate of Origin) and `cla_required` (a Contributor License Agreement must be signed).
4. `target_branch`: the branch PRs must target, if the guidelines name one other than the default; otherwise "".
5. `title`, `commit_subject`, `commit_body`, `branch_name`: rewrite the draft's to follow the repository's conventions (e.g. Conventional Commits `docs(scope): ...`, length limits, branch naming rules). If there is no convention, keep the draft's. `commit_body` must not contain Signed-off-by lines; those are added separately.
6. `body`: if there is a pull request template, fill it in: keep its headings, order and checkboxes, put the draft's facts into the matching sections, and delete only instructional comments the template says to delete. If there are several templates, pick the one that fits a docs change and name it in `template_used`. If there is no template, use the draft body, adapted to any explicit body requirements. Always keep a clear statement that the change was found and verified by an automated tool using an AI agent. Keep the draft's closing note (the horizontal rule and the line starting "This pull request was prepared by rtfm.run") at the very end of the body, after every template section.
7. `checkboxes`: list every checkbox in your body. Set `checked` only when it is objectively true of this change from the facts above (e.g. "documentation only", "no code changes", "I have tested the change" is true only in the sense that the automated verification ran; say so in `reason`). Anything that is a personal statement by the human submitting the PR ("I have read the contributing guide", "I agree to the code of conduct", "I signed the CLA", "I tested locally") must be `checked: false` with `needs_human: true`. In `body`, every `needs_human` box must be unchecked.
8. `other_requirements`: any other rule in the guidelines that applies to this PR (labels, linked issues, changelog entries, tests, screenshots...), whether this PR satisfies it, and how.
9. `notes`: short bullet points on how you applied the guidelines.

Never invent facts: no made-up issue numbers, test results, or claims beyond the draft. When the guidelines are ambiguous, choose the more conservative reading and mention it in `notes`.
