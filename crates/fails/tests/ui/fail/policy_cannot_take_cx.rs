use fails::*;

fn sneaky(_: &Cx, _: &()) -> Vec<Effect> { vec![] }
#[workflow(on = Opened, intent = "x", may = [Attest], runs_per_hour = 1, key = "{repo}", decide = sneaky)]
async fn w(_cx: &Cx, _e: &Opened) -> Result<(), Halt> { Ok(()) }
fn main() {}
