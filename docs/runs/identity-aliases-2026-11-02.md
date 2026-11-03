---
type: Run
title: Author bylines - Eli Lap = Ilja Lapkovsky = Ilja Lapkovskis
description: Taught the grounded prompt the author alias mapping, added a one-shot search-first nudge, and verified it live - Ilja byline questions cited LinkedIn cross-posts, both identity attempts answered with citations, and the harness answer now includes both Ghost and Postiz sources.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-02T22:00:00Z
run:
  type: agent-ask-identity
  cmd: ./agent ask --timeout 120s "What did Ilja Lapkovsky write about harnesses?"
output: |
  $ ./agent ask --format json "What did Ilja Lapkovsky write about harnesses?"
  {"refused":false,"tool_iterations":1,"elapsed_ms":21797,
   "citations":[
     "https://read.elilap.dev/i-gave-up-on-zero-harness-and-here-is-why/",
     "https://read.elilap.dev/i-gave-up-on-zero-harness-and-here-is-why/",
     "https://www.linkedin.com/feed/update/urn:li:share:7495497314913034240",
     "https://www.linkedin.com/feed/update/urn:li:share:7523034784047165441"],
   "answer":"Ilja Lapkovsky (Eli Lap) has written about harnesses primarily in
   his post \"I gave up on zero harness and here is why\" ... plus a couple of
   related LinkedIn posts."}

  $ ./agent ask --format json "Is Eli Lap the same person as Ilja Lapkovsky and Ilja Lapkovskis?"
  {"refused":false,"answer":"Yes. The excerpts are consistent with Eli Lap, Ilja
   Lapkovsky, and Ilja Lapkovskis being the same person. ... Identity fact (not
   an excerpt): ...", "citations":[ ... ]}

  $ ./agent ask --format json "What does Eli say about harnesses?"
  hosts: read.elilap.dev, read.elilap.dev, www.linkedin.com,
         www.linkedin.com, read.elilap.dev
---

# Result

Before this change the model saw Postiz chunks authored as "Ilja Lapkovsky"
next to Ghost chunks authored as "Eli Lap" and treated the cross-posts as
someone else's writing. During the first harness run it said so explicitly:
"The linkedin post and other excerpts about harnesses appear to be by Ilja
Lapkovsky, not Eli Lap, and were not used."

The grounded prompt now carries one service identity fact: Eli Lap, Ilja
Lapkovsky, and Ilja Lapkovskis are the same person; "Eli Lap" is the
internationalized pen name and the other two are the same author's name. The
prompt also tells the model never to treat a name difference as evidence that
a source belongs to someone else. The same mapping is in the tool description,
so the model knows `search_writing` covers both bylines. Corpus metadata was
left untouched: Ghost chunks stay "Eli Lap", Postiz chunks stay "Ilja
Lapkovsky", and the index remains a faithful derived artifact.

Live verification:

* The Ilja byline question answered without refusal and cited two LinkedIn
  cross-post chunks alongside two Ghost chunks.
* The direct identity question answered "Yes" and grounded the claim in
  retrieved excerpts that show both bylines linking to the same content, while
  labelling the service identity fact as not an excerpt.
* The original harness question now draws on LinkedIn cross-posts as well:
  two of its five citations are `www.linkedin.com`.

The identity fact is the only non-excerpt grounding the prompt allows; answers
still need a retrieved `chunk_id` or they collapse to the refusal string.

## Follow-up: the search-first nudge

Two of the first HTTP identity attempts still refused because the model
answered without calling the tool at all (`tool_iterations: 0`). The loop now
nudges exactly once when the first response is the refusal string with zero
tool iterations: it appends the refusal as the assistant turn and a user
instruction to search before refusing, then continues. Off-corpus questions
pay one extra retrieval call; identity questions get the evidence they need.

After the nudge, two consecutive HTTP identity attempts both answered:

```text
{"refused":false,"tool_iterations":2,"citations":3,"answer":"Yes - the excerpts point to them being the same author. ..."}
{"refused":false,"tool_iterations":2,"citations":3,"answer":"Yes. The excerpts returned contain writing attributed to \"Ilja Lapkovsky\" ..."}
```

The off-corpus control still refused, now after three retrieval rounds:

```text
{"refused":true,"tool_iterations":3,"citations":0,"answer":"Not in my published writing"}
```
