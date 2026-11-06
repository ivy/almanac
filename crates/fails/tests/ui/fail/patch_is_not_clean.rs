use fails::*;

async fn gather(cx: &Cx, fixed: &Patch) -> Result<Done, Halt> {
    cx.agent(WRITER).act(WritePrompt, fixed).await
}

fn main() { let _ = gather; }
