use fails::*;

#[workflow(on = Opened, intent = "x", may = [Attest], runs_per_hour = 1, key = "{nope}", decide = no_effects)]
async fn w(_cx: &Cx, _e: &Opened) -> Result<(), Halt> { Ok(()) }
fn main() {}
