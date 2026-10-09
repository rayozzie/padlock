# Padlock Wiki

Welcome to the Padlock Wiki! This wiki provides comprehensive documentation about the Padlock project, a threshold data-splitting utility for secure data archiving.

## What is Padlock?

Padlock is a high-performance K-of-N threshold data encoding and decoding utility that implements a one-time-pad scheme for secure data archiving and border-crossings. It was created by Ray Ozzie.

Padlock uses random pads and XOR to split data into collections, with K intact collections from the same backup needed for recovery. Confidentiality depends on the pad randomness. Its default combines five generator algorithms sharing one OS randomness source; it does not establish independent entropy or information-theoretic secrecy. See the [Security Model](Security-Model) for the assumptions and limitations.

## Wiki Navigation

* [Overview](Overview)
* [Architecture](Architecture)
* [Usage Guide](Usage-Guide)
* [Security Model](Security-Model)
* [Implementation Details](Implementation-Details)
