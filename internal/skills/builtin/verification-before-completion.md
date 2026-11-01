---
name: verification-before-completion
description: Check the real result of every approved command before you report that the work is done.
version: 1.0.0
roles: verifier
when: always
license: MIT
source: orqen-builtin; idea from obra/superpowers (MIT)
---
# Verification before completion

A claim of "done" needs evidence from this round, not from memory.

1. List the acceptance criteria and the approved commands before you start.
2. Run each approved command yourself and read its real output to the end. Note the exit code.
3. Match every criterion to a line of evidence. A criterion without evidence is unverified, not passed.
4. If a command fails, report the first error and the command that shows it. Do not retry until it passes.
5. Never report a pass for something you did not run. Say what you could not run and why.
6. Keep the verdict short and put the evidence under it, one line per command or criterion.
