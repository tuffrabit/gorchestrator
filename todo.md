# TODO

- ~~view each agent's output/error via drawer, not just indeterminate state~~ (phase tabs + per-phase result/output/activity; implementer workspace tree)
- ~~allow file upload in new issue prompt~~ (text-like attachments on submit)
- ~~difference between issue title and issue description/prompt?~~ (optional description + issue.md)
- ~~download implementer output~~ (workspace.zip when implementation done)
- ~~project should be configurable not as issue input — planned: `phase_4_project_refactor.md` / spec §6.0~~
- ~~agents should be configurable per project — planned: flavors in `phase_4_project_refactor.md` / spec §8.2~~
- users should be scoped to project — deferred (spec §17 Q17)
- users should be included via email invite — deferred (spec §17 Q17)
- agent should be selectable via new issue if there are multiple choices — planned: `phase_4_project_refactor.md`
- ~~ui issue. issue cards should be full width.~~
- ui issue. dry run checkbox miss-alignment on new issue drawer.
- app start needs to look for projects in db that are no longer in config and disable them, which means we need to implement a project is_active flag or something
- introduce concept in config of "resource constrained inference". where the orchestrator only allows one agent to run at a time. perhaps the orchestrator could even unload and load different models via api call to local inference server to facilitate different agent flavors in between agent runs. this would likely require another app that servces as a llama.cpp or vllm proxy to manage the model loading and unloading.
- "no issues yet" ui remains after creating a new issue
- per agent/provider token limit
