use fails::*;

#[workflow(on = Opened, key = "{repo}", intent = "x", may = [Attest], runs_per_hour = 1, decide = no_effects)]
async fn w(_cx: &Cx, _e: &Opened) -> Result<(), Halt> { Ok(()) }

fn main() { let _ = W; }
