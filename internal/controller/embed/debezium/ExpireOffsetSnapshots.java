// Expires old snapshots of a Debezium Server offset table.
//
// The Iceberg offset store overwrites its table on every offset commit, adding
// a snapshot each time, and nothing expires them. Table metadata grows with
// every snapshot, so commits slow down. Once a commit outlasts
// offset.flush.timeout.ms, the engine stops confirming the replication slot.
//
// It builds the catalog the way the offset store does: its own
// debezium.source.offset.storage.iceberg.* keys first, then the sink's
// debezium.sink.iceberg.* keys. It runs in the Debezium Server image with the
// image's libraries, so the Iceberg version always matches the engine's.
//
// Usage: java -cp '/debezium/lib/*' ExpireOffsetSnapshots.java \
//          <application.properties> <retain-ms> <retain-last>

import java.io.FileInputStream;
import java.io.InputStream;
import java.util.HashMap;
import java.util.Map;
import java.util.Properties;
import org.apache.iceberg.CatalogUtil;
import org.apache.iceberg.Snapshot;
import org.apache.iceberg.Table;
import org.apache.iceberg.catalog.Catalog;
import org.apache.iceberg.catalog.Namespace;
import org.apache.iceberg.catalog.TableIdentifier;

public class ExpireOffsetSnapshots {
  private static final String OFFSET_PREFIX = "debezium.source.offset.storage.iceberg.";
  private static final String SINK_PREFIX = "debezium.sink.iceberg.";

  public static void main(String[] args) throws Exception {
    if (args.length != 3) {
      System.err.println("usage: ExpireOffsetSnapshots <application.properties> <retain-ms> <retain-last>");
      System.exit(2);
    }
    Properties file = new Properties();
    try (InputStream in = new FileInputStream(args[0])) {
      file.load(in);
    }
    long retainMs = Long.parseLong(args[1]);
    int retainLast = Integer.parseInt(args[2]);

    Map<String, String> props = new HashMap<>();
    subset(file, OFFSET_PREFIX, props);
    subset(file, SINK_PREFIX, props);
    String catalogName = props.getOrDefault("catalog-name", "default");
    String namespace = props.getOrDefault("table-namespace", "");
    TableIdentifier id = TableIdentifier.of(
        namespace.isEmpty() ? Namespace.of("default") : Namespace.of(namespace.split("\\.")),
        props.getOrDefault("table-name", "debezium_offset_storage"));

    Catalog catalog = CatalogUtil.buildIcebergCatalog(
        catalogName, props, new org.apache.hadoop.conf.Configuration());
    try {
      if (!catalog.tableExists(id)) {
        System.out.println("Offset table " + id + " does not exist yet");
        return;
      }
      Table table = catalog.loadTable(id);
      int before = count(table.snapshots());
      long started = System.currentTimeMillis();
      // Expiry also deletes the data and manifest files only the expired
      // snapshots referenced (the default cleanup level).
      table.expireSnapshots()
          .expireOlderThan(started - retainMs)
          .retainLast(retainLast)
          .commit();
      table.refresh();
      int after = count(table.snapshots());
      System.out.printf("Expired %d snapshot(s) of %s in %d ms; %d remain%n",
          before - after, id, System.currentTimeMillis() - started, after);
    } finally {
      if (catalog instanceof AutoCloseable closeable) {
        closeable.close();
      }
    }
  }

  // Copies keys with the prefix, minus the prefix, without overwriting.
  private static void subset(Properties file, String prefix, Map<String, String> out) {
    for (String key : file.stringPropertyNames()) {
      if (key.startsWith(prefix)) {
        out.putIfAbsent(key.substring(prefix.length()), file.getProperty(key));
      }
    }
  }

  private static int count(Iterable<Snapshot> snapshots) {
    int n = 0;
    for (Snapshot ignored : snapshots) {
      n++;
    }
    return n;
  }
}
