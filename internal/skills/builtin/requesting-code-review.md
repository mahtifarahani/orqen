---
name: requesting-code-review
description: Review a change against its acceptance criteria and report findings with a clear severity.
version: 1.0.0
roles: reviewer
when: always
license: MIT
source: orqen-builtin; idea from obra/superpowers (MIT)
---
# Reviewing a change

1. Read the acceptance criteria first, then the diff. Judge the diff against the criteria.
2. Check correctness, missing cases, and whether tests still test what they claim to.
3. Give each finding a file, a line, a severity and what to change. One finding, one problem.
4. Use blocker or major only for defects that must be fixed before the change can be used.
5. Do not report style preferences as defects. Mark small matters as minor or nit.
6. State what you did not review, so the scope of your verdict is clear.
7. Do not edit files. Describe the fix and leave the change to the author.
