use almanac::store::{MemoryStore, Store};
use almanac::testing::conformance;
use std::collections::HashMap;
use std::sync::{Arc, Mutex};

#[tokio::test]
async fn memory_store_conforms() {
    let scopes: Mutex<HashMap<String, Arc<MemoryStore>>> = Mutex::default();
    conformance::all(|scope| {
        let mut scopes = scopes.lock().unwrap();
        let store = scopes.entry(scope.to_string()).or_default();
        store.clone() as Arc<dyn Store>
    })
    .await;
}
