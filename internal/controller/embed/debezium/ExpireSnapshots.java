// Expires old snapshots of the Iceberg tables one Debezium Server writes.
//
// The sink commits to each table about once a minute, and the Iceberg offset
// store overwrites its table on every offset commit. Each commit adds a
// snapshot, and nothing expires them. Table metadata grows with every
// snapshot, so commits slow down. Once an offset commit outlasts
// offset.flush.timeout.ms, the engine stops confirming the replication slot.
//
// Every table in the source's namespace is expired: the offset table with the
// offset retention, the rest with the data retention. A table that was never
// expired can hold tens of thousands of snapshots, and removing them all in
// one commit holds catalog transactions for minutes, so each commit removes
// at most BATCH, oldest first.
//
// It builds the catalog the way the offset store does: its own
// debezium.source.offset.storage.iceberg.* keys first, then the sink's
// debezium.sink.iceberg.* keys. It runs in the Debezium Server image with the
// image's libraries, so the Iceberg version always matches the engine's.
//
// Usage: java -cp '/debezium/lib/*' ExpireSnapshots.java \
//          <application.properties> <offset-retain-ms> <data-retain-ms> <retain-last>
//
// Exits 1 when any table failed; the others are still expired.

import java.io.FileInputStream;
import java.io.InputStream;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import org.apache.iceberg.CatalogUtil;
import org.apache.iceberg.Snapshot;
import org.apache.iceberg.Table;
import org.apache.iceberg.catalog.Catalog;
import org.apache.iceberg.catalog.Namespace;
import org.apache.iceberg.catalog.TableIdentifier;

public class ExpireSnapshots {
  private static final String OFFSET_PREFIX = "debezium.source.offset.storage.iceberg.";
  private static final String SINK_PREFIX = "debezium.sink.iceberg.";
  private static final int BATCH = 1000;

  public static void main(String[] args) throws Exception {
    if (args.length != 4) {
      System.err.println(
          "usage: ExpireSnapshots <application.properties> <offset-retain-ms> <data-retain-ms> <retain-last>");
      System.exit(2);
    }
    Properties file = new Properties();
    try (InputStream in = new FileInputStream(args[0])) {
      file.load(in);
    }
    long offsetRetainMs = Long.parseLong(args[1]);
    long dataRetainMs = Long.parseLong(args[2]);
    int retainLast = Integer.parseInt(args[3]);

    Map<String, String> props = new HashMap<>();
    subset(file, OFFSET_PREFIX, props);
    subset(file, SINK_PREFIX, props);
    String catalogName = props.getOrDefault("catalog-name", "default");
    String nsName = props.getOrDefault("table-namespace", "");
    Namespace namespace = nsName.isEmpty() ? Namespace.of("default") : Namespace.of(nsName.split("\\."));
    String offsetTable = props.getOrDefault("table-name", "debezium_offset_storage");

    Catalog catalog = CatalogUtil.buildIcebergCatalog(
        catalogName, props, new org.apache.hadoop.conf.Configuration());
    int failures = 0;
    try {
      List<TableIdentifier> tables = new ArrayList<>(catalog.listTables(namespace));
      // The offset table first: it is the one that stalls the slot.
      tables.sort((a, b) -> Boolean.compare(!a.name().equals(offsetTable), !b.name().equals(offsetTable)));
      for (TableIdentifier id : tables) {
        long retainMs = id.name().equals(offsetTable) ? offsetRetainMs : dataRetainMs;
        try {
          long started = System.currentTimeMillis();
          Table table = catalog.loadTable(id);
          int[] result = expire(table, started - retainMs, retainLast);
          if (result[0] > 0) {
            System.out.printf("Expired %d snapshot(s) of %s in %d commit(s), %d ms; %d remain%n",
                result[0], id, result[1], System.currentTimeMillis() - started, result[2]);
          }
        } catch (Exception e) {
          failures++;
          System.out.println("Expiring snapshots of " + id + " failed: " + e);
        }
      }
    } finally {
      if (catalog instanceof AutoCloseable closeable) {
        closeable.close();
      }
    }
    if (failures > 0) {
      System.exit(1);
    }
  }

  // Returns {removed, commits, remaining}. Expiry also deletes the data and
  // manifest files only the expired snapshots referenced (the default
  // cleanup level).
  private static int[] expire(Table table, long cutoff, int retainLast) {
    int removed = 0;
    int commits = 0;
    while (true) {
      List<Long> times = timestamps(table);
      int older = 0;
      while (older < times.size() && times.get(older) < cutoff) {
        older++;
      }
      int expirable = Math.min(older, times.size() - retainLast);
      if (expirable <= 0) {
        return new int[] {removed, commits, times.size()};
      }
      // expireOlderThan is exclusive, so this removes at most BATCH.
      long bound = expirable > BATCH ? Math.max(times.get(BATCH), times.get(0) + 1) : cutoff;
      table.expireSnapshots()
          .expireOlderThan(Math.min(bound, cutoff))
          .retainLast(retainLast)
          .commit();
      commits++;
      table.refresh();
      int after = count(table.snapshots());
      if (after >= times.size()) {
        // Nothing went, e.g. the rest are kept by a branch or tag.
        return new int[] {removed, commits, after};
      }
      removed += times.size() - after;
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

  private static List<Long> timestamps(Table table) {
    List<Long> times = new ArrayList<>();
    for (Snapshot s : table.snapshots()) {
      times.add(s.timestampMillis());
    }
    Collections.sort(times);
    return times;
  }

  private static int count(Iterable<Snapshot> snapshots) {
    int n = 0;
    for (Snapshot ignored : snapshots) {
      n++;
    }
    return n;
  }
}
