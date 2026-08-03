# Agent Working Preferences

## Rule 1: No Unsolicited Markdown Files

Do not create markdown files for thoughts, analyses, or documentation unless explicitly requested.

If you must write unsolicited content, put it in `.thoughts/agents/` (gitignored, throwaway content, not valuable).

Don't create: README.md, NOTES.md, PLAN.md, documentation files, analysis files without being asked.

## Rule 2: Planning Without Fluff

When planning, never include unless explicitly requested:
- Time estimates
- Cost estimates
- Documentation as a deliverable
- Deployment steps
- CI/CD pipeline work

Focus only on implementation steps, technical work breakdown, and dependencies.

Bad: "1. Implement X (2 hrs), 2. Write tests (1 hr), 3. Write docs, 4. Deploy"
Good: "1. Implement X, 2. Write tests"

## Rule 3: Code Must Be Verified - No Excuses

NEVER justify that something should work just because you wrote code. "This code does X so it should work" is not acceptable.

Code must ALWAYS be verified by actually running it. If you cannot get it running, say so. If it doesn't work, say so.

Do not:
- Claim code works without testing it
- Explain why code "should" work when it hasn't been verified
- Justify theoretical correctness without practical verification
- Lie about functionality

If you lie about code working without verification, you will get /scoldilocks.

Only facts. Only verified results. No theoretical justifications.

## Rule 4: Load Secrets from .envrc

If secrets or environment variables cannot be found during execution, check for local `.envrc` files and load them into your shell session.

Use `source .envrc` or direnv to load environment variables before running commands that need them.

## Rule 5: User-Facing Text Is Simplified Technical English

Every string a human or an agent reads is written in ASD-STE100 Simplified
Technical English. That is: CLI `Short`/`Long`/flag usage, MCP tool and
parameter descriptions, error messages, warnings, the `prime` briefing, the
generated `AGENTS.md`, and the README.

The rules that matter most here:

- One sentence, one idea. 20 words maximum for an instruction, 25 for a
  description.
- Active voice. Name who does what: "devstack builds the replica", not "the
  replica is built".
- One word, one meaning. A concept keeps the same name everywhere — do not
  alternate between "copy" and "instance" for the same thing.
- Simple tenses. Say "the command refuses", not "the command will have refused".
- Condition before command: "If the stack is down, run `devstack stack up`".
- No noun clusters longer than three words.

Two things this rule does NOT license:

- Do not drop a warning to meet a word count. Split it into two sentences and
  keep the condition next to its consequence. A shorter sentence that loses the
  reason a command is dangerous is a worse sentence.
- Do not simplify a term of art into something vaguer. `worktree`, `overlay`,
  `replica` and `stack` are precise; keep them and define them once.

This is a readability rule, not a rewriting licence: fix the language, never
the meaning.
