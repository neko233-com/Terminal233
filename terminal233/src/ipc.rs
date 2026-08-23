//! Small typed event channel used by the rdesktop native bridge.
//!
//! rdesktop transports JSON across the WebView boundary. Keeping this adapter in the client
//! means SSH, SFTP, and PTY workers never depend on a UI framework's IPC runtime.

use std::{fmt::Display, marker::PhantomData, sync::Arc};

use serde::{Serialize, de::DeserializeOwned};

#[derive(Clone, Debug)]
pub struct Message(serde_json::Value);

impl Message {
    pub fn deserialize<T: DeserializeOwned>(&self) -> Result<T, serde_json::Error> {
        serde_json::from_value(self.0.clone())
    }
}

#[derive(Clone)]
pub struct Channel<T> {
    send: Arc<dyn Fn(Message) -> Result<(), String> + Send + Sync>,
    marker: PhantomData<fn(T)>,
}

impl<T> Channel<T> {
    pub fn new<F, E>(callback: F) -> Self
    where
        F: Fn(Message) -> Result<(), E> + Send + Sync + 'static,
        E: Display,
    {
        Self {
            send: Arc::new(move |message| callback(message).map_err(|error| error.to_string())),
            marker: PhantomData,
        }
    }

    pub fn send(&self, value: T) -> Result<(), String>
    where
        T: Serialize,
    {
        let message = serde_json::to_value(value).map_err(|error| error.to_string())?;
        (self.send)(Message(message))
    }
}
