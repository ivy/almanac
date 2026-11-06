use fails::*;

#[prompt(text = "fix")]
pub struct FixPrompt;

const FIXER: AgentSpec<Fixer, FixPrompt> = AgentSpec::fixer("fixer");

async fn gather(cx: &Cx, input: &Untrusted<String>) -> Result<Untrusted<String>, Halt> {
    cx.agent(FIXER).repair(FixPrompt, input).await
}

fn main() { let _ = gather; }
