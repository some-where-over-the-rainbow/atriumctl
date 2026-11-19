# Question — decompose the task

Transform the task below into 3-7 specific, neutral research questions. These questions drive the next phase (Research), which runs in a separate context with **no knowledge of what is being built**.

## Task

{{.Task}}
{{if .Feedback}}
## Feedback from Research

A previous Research run found the questions insufficient:

{{.Feedback}}

Revise the questions accordingly. Questions the architect already approved or rejected keep their status and stay in the file; you cannot change them. Add new questions with new ids, as `proposed`.
{{end}}
## Process

1. Explore the codebase lightly to see what exists: use `ls`, `glob` and `grep`, or `spawn` a locator sub-agent with a short, neutral request ("Find where X lives. Describe what exists."). You need to know what exists to write good questions.
2. Decompose the task into 3-7 research questions:
   - Each question makes a researcher explore a different relevant area of the codebase.
   - Questions are **neutral**: they ask what exists and how it works, never how to build something.
   - Prefer "trace the flow" questions that reveal architecture over yes/no questions.

   Good: "How does the middleware chain handle request authentication, and where are auth policies defined?"
   Bad: "What's the best way to add a new authenticated endpoint?"
3. Write `.atriumctl/task.md`: a clean 2-3 sentence description of what is being built and why. Later phases read it; Research never does.
4. Write `.atriumctl/questions.md` (read it first; it may exist and be empty):

   ````markdown
   # Research Questions

   ## Context
   [2-3 sentences on which areas of the codebase to focus on. Do NOT mention what is being built or why.]

   ## Questions

   ```{q} [Neutral, fact-seeking question as the title]
   :id: Q_001
   :status: proposed
   :area: [short area name, e.g. player, routing, storage]

   [One or two sentences that sharpen the question, if needed.]
   ```

   ```{q} ...
   :id: Q_002
   ...
   ```
   ````

   Ids are `Q_` and three digits, unique. Status is always `proposed`: only the architect approves.
5. Run `needs_lint` and fix every error it reports.
6. Reply with one line per question: its id and title.

## Rules

- `questions.md` must NOT contain the task description, goals or desired behaviour. The researcher who reads it should have no idea what feature is being built.
- Each question targets a different area or concern.
- If the task is too simple for 3 questions, still write at least one, and say so in your reply.
