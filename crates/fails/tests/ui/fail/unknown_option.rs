use fails::*;

#[workflow(on = Opened, intent = "x", may = [Attest], runs_per_hour = 1, key = "{repo}", decide = no_effects, retries = 3)]
async fn w(_cx: &Cx, _e: &Opened) -> Result<(), Halt> { Ok(()) }
fn main() {}
